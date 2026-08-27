package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEffectFinalDeterministicGuards(t *testing.T) {
	t.Run("committed retained original failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		transaction.effects.sourceQuarantined[0] = true
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent, transaction.journal, transaction.effects,
		); err == nil {
			t.Fatal("invalid retained original verified as committed")
		}
	})

	t.Run("committed postimage links failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		if err := transaction.publishTargets(); err != nil {
			t.Fatal(err)
		}
		operation := transaction.intent.operations[0]
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(operation.targetAnchor.canonical, witness.Rooted.Basename)); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent, transaction.journal, transaction.effects,
		); err == nil {
			t.Fatal("committed target without witness verified")
		}
	})

	t.Run("committed forest verification failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			nil,
		)
		if err := transaction.publishTargets(); err != nil {
			t.Fatal(err)
		}
		forest := transaction.intent.forests[0]
		if err := os.WriteFile(
			filepath.Join(forest.anchorPath, forest.publicRoot, "result.txt"),
			[]byte("changed\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent, transaction.journal, transaction.effects,
		); err == nil {
			t.Fatal("corrupt committed forest verified")
		}
	})

	t.Run("retained original metadata unavailable", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		if err := verifyApplyPatchTxnRetainedOriginal(
			transaction.intent.operations[0], transaction.journal,
		); err == nil {
			t.Fatal("uncheckpointed retained original verified")
		}
	})

	t.Run("retained quarantine changed", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		operation := transaction.intent.operations[0]
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(operation.source.anchor.canonical, quarantine.Rooted.Basename),
			[]byte("changed\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnRetainedOriginal(operation, transaction.journal); err == nil {
			t.Fatal("changed retained quarantine verified")
		}
	})

	t.Run("retained witness identity changed", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		operation := transaction.intent.operations[0]
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
		witnessPath := filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename)
		if err := os.Remove(witnessPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(witnessPath, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		quarantine.Rooted.Links = 1
		witness.Rooted.Links = 1
		if err := verifyApplyPatchTxnRetainedOriginal(operation, transaction.journal); err == nil {
			t.Fatal("replacement retained witness verified")
		}
	})

	t.Run("postimage witness lookup failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		operation := transaction.intent.operations[0]
		if err := operation.targetAnchor.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := committedApplyPatchTxnPostimageLinks(
			operation, transaction.journal, false,
		); err == nil {
			t.Fatal("closed-anchor postimage witness lookup succeeded")
		}
	})

	t.Run("committed source cleanup identity conflict", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		quarantine.Rooted.Identity.File++
		if err := transaction.cleanupCommittedPublicArtifacts(); err == nil {
			t.Fatal("source cleanup with wrong identity succeeded")
		}
	})

	t.Run("committed forest cleanup identity conflict", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			nil,
		)
		forestIntent := transaction.intent.forests[0]
		forest, err := requireApplyPatchTxnJournalForest(transaction.journal, forestIntent.id)
		if err != nil {
			t.Fatal(err)
		}
		forest.SentinelWitness.Identity.File++
		if err := transaction.cleanupCommittedPublicArtifacts(); err == nil {
			t.Fatal("forest cleanup with wrong identity succeeded")
		}
	})

	t.Run("forest root identity unavailable", func(t *testing.T) {
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
		forest.SentinelWitness.Identity = nil
		forest.Entries = forest.Entries[:1]
		forest.StageRoot.Identity = nil
		if err := removeApplyPatchTxnForestTree(
			intent, forest, intent.stageRoot, func() error { return nil },
		); err == nil {
			t.Fatal("forest removal without root identity succeeded")
		}
	})

	t.Run("forest root removal identity conflict", func(t *testing.T) {
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
		forest.SentinelWitness.Identity = nil
		forest.Entries = forest.Entries[:1]
		forest.StageRoot.Identity.File++
		if err := removeApplyPatchTxnForestTree(
			intent, forest, intent.stageRoot, func() error { return nil },
		); err == nil {
			t.Fatal("forest root removal with wrong identity succeeded")
		}
	})

	t.Run("committed cleanup phase guard", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		if err := transaction.cleanupCommitted(); err == nil {
			t.Fatal("prepared transaction entered committed cleanup")
		}
	})
}

func TestApplyPatchTransactionEffectFinalRollbackGuards(t *testing.T) {
	t.Run("private artifact anchor identity changed", func(t *testing.T) {
		directory := t.TempDir()
		if err := verifyApplyPatchTxnRollbackPrivateResidueAbsent(
			&applyPatchTxnIntentPlan{},
			&applyPatchTransactionJournal{Artifacts: []applyPatchTransactionJournalArtifact{{
				Rooted: &applyPatchTransactionJournalRootedLocation{
					AnchorCanonicalPath: directory,
				},
			}}},
		); err == nil {
			t.Fatal("changed private artifact anchor verified")
		}
	})

	t.Run("private forest metadata unavailable", func(t *testing.T) {
		if err := verifyApplyPatchTxnRollbackPrivateResidueAbsent(
			&applyPatchTxnIntentPlan{forests: []*applyPatchTxnForestIntent{{id: "missing"}}},
			&applyPatchTransactionJournal{},
		); err == nil {
			t.Fatal("missing private forest metadata verified")
		}
	})

	t.Run("private forest residue", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "residue"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		anchor, err := openApplyPatchTxnAnchor(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer anchor.Close()
		if err := verifyApplyPatchTxnRollbackPrivateResidueAbsent(
			&applyPatchTxnIntentPlan{forests: []*applyPatchTxnForestIntent{{
				id: "forest", anchor: anchor,
			}}},
			&applyPatchTransactionJournal{Forests: []applyPatchTransactionJournalForest{{
				ID: "forest",
				StageRoot: applyPatchTransactionJournalRootedLocation{
					Basename: "residue", RemovalBasename: "absent-removal",
				},
			}}},
		); err == nil {
			t.Fatal("private forest residue verified absent")
		}
	})

	t.Run("rollback source metadata unavailable", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err := verifyApplyPatchTxnRolledBackPublicState(
			transaction.intent, transaction.journal, false,
		); err == nil {
			t.Fatal("rollback source without restore metadata verified")
		}
	})

	t.Run("rollback witness lookup failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		operation := transaction.intent.operations[0]
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSource(operation); err != nil {
			t.Fatal(err)
		}
		if err := operation.source.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnRolledBackPublicState(
			transaction.intent, transaction.journal, true,
		); err == nil {
			t.Fatal("rollback witness on closed anchor verified")
		}
	})

	t.Run("rollback public source changed", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		if err := os.WriteFile(
			transaction.intent.operations[0].planned.sourcePath,
			[]byte("changed\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnRolledBackPublicState(
			transaction.intent, transaction.journal, false,
		); err == nil {
			t.Fatal("changed rollback source verified")
		}
	})

	t.Run("rollback restore witness missing", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		operation := transaction.intent.operations[0]
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSource(operation); err != nil {
			t.Fatal(err)
		}
		restore, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		restore.Rooted.Identity = copyApplyPatchTxnIdentity(operation.source.state.Identity)
		restore.Rooted.Links = 2
		if err := verifyApplyPatchTxnRolledBackPublicState(
			transaction.intent, transaction.journal, true,
		); err == nil {
			t.Fatal("missing rollback restore witness verified")
		}
	})

	t.Run("rollback forest public residue", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			nil,
		)
		forest := transaction.intent.forests[0]
		if err := os.Mkdir(filepath.Join(forest.anchorPath, forest.publicRoot), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnRolledBackPublicState(
			transaction.intent, transaction.journal, false,
		); err == nil {
			t.Fatal("rollback forest public residue verified absent")
		}
	})

	t.Run("restore source quarantine lookup failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		operation := transaction.intent.operations[0]
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		if err := operation.source.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSource(operation); err == nil {
			t.Fatal("source restore on a closed anchor succeeded")
		}
	})

	t.Run("restore source witness identity changed", func(t *testing.T) {
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
		witnessPath := filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename)
		if err := os.Remove(witnessPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(witnessPath, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		quarantine.Rooted.Links = 1
		witness.Rooted.Links = 1
		if err := transaction.restoreSource(operation); err == nil {
			t.Fatal("source restore with replacement witness succeeded")
		}
	})

	t.Run("old witness final checkpoint failure", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		operation := transaction.intent.operations[0]
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSource(operation); err != nil {
			t.Fatal(err)
		}
		injected := errors.New("final witness checkpoint failed")
		writes := 0
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" {
				writes++
				if writes == 3 {
					return injected
				}
			}
			return nil
		}
		if err := transaction.cleanupRollbackSourceWitnesses(); !errors.Is(err, injected) {
			t.Fatalf("old witness final checkpoint error = %v", err)
		}
	})
}

func TestApplyPatchTransactionEffectFinalBackupRestoreGuards(t *testing.T) {
	t.Run("initialize restore-required effects", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		transaction.effects.sourceRestoreRequired = nil
		injected := errors.New("stop after effects initialization")
		transaction.fault = func(boundary string) error {
			if boundary == "restore_create_before_identity:0" {
				return injected
			}
			return nil
		}
		if err := transaction.restoreSourceFromBackupV2(operation); !errors.Is(err, injected) {
			t.Fatalf("restore initialization error = %v", err)
		}
		if transaction.effects.sourceRestoreRequired == nil {
			t.Fatal("restore-required effects were not initialized")
		}
	})

	t.Run("restore stage create conflict", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		if err := os.Mkdir(
			filepath.Join(operation.source.anchor.canonical, restore.Rooted.Basename),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("restore stage directory collision succeeded")
		}
	})

	t.Run("restore identity checkpoint failure", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		injected := errors.New("restore identity checkpoint failed")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" {
				return injected
			}
			return nil
		}
		if err := transaction.restoreSourceFromBackupV2(operation); !errors.Is(err, injected) {
			t.Fatalf("restore identity checkpoint error = %v", err)
		}
	})

	t.Run("resumed restore witness unavailable", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		createApplyPatchTxnEffectFinalRestoreStage(t, operation, restore, []byte("bef"))
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal, operation.index, applyPatchTransactionArtifactSourceWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename)); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("resumed restore without old witness succeeded")
		}
	})

	t.Run("resumed restore stage bounds conflict", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		before := transaction.journal.Operations[operation.index].Before
		createApplyPatchTxnEffectFinalRestoreStage(
			t, operation, restore, make([]byte, before.Length+1),
		)
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("oversized resumed restore stage succeeded")
		}
	})

	t.Run("resumed restore content conflict", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		createApplyPatchTxnEffectFinalRestoreStage(t, operation, restore, []byte("wrong"))
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("wrong-prefix resumed restore stage succeeded")
		}
	})

	t.Run("late restore witness validation", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		transaction.fault = func(boundary string) error {
			if boundary != "restore_stage_synced:0" {
				return nil
			}
			witness, err := requireApplyPatchTxnArtifact(
				transaction.journal, operation.index,
				applyPatchTransactionArtifactSourceWitness,
			)
			if err != nil {
				return err
			}
			return os.Remove(filepath.Join(
				operation.source.anchor.canonical, witness.Rooted.Basename,
			))
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("restore with witness removed after staging succeeded")
		}
	})

	t.Run("late quarantine residue", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(operation.source.anchor.canonical, quarantine.Rooted.Basename),
			[]byte("alien"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("backup restore over quarantine residue succeeded")
		}
	})

	t.Run("restore publication collision", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		transaction.fault = func(boundary string) error {
			if boundary == "restore_stage_synced:0" {
				return os.WriteFile(operation.planned.sourcePath, []byte("alien"), 0o600)
			}
			return nil
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("backup restore publication collision succeeded")
		}
	})

	t.Run("restore publication checkpoint failure", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		injected := errors.New("restore publication checkpoint failed")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" && restore.Rooted.Links == 2 {
				return injected
			}
			return nil
		}
		if err := transaction.restoreSourceFromBackupV2(operation); !errors.Is(err, injected) {
			t.Fatalf("restore publication checkpoint error = %v", err)
		}
	})

	t.Run("restore postpublication corruption", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		transaction.fault = func(boundary string) error {
			if boundary == "restore_published:0" {
				return os.WriteFile(operation.planned.sourcePath, []byte("changed"), 0o600)
			}
			return nil
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("corrupt published restore source succeeded")
		}
	})

	t.Run("finish restore quarantine residue", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(operation.source.anchor.canonical, quarantine.Rooted.Basename),
			[]byte("alien"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.finishBackupRestoredSourceCleanup(operation, restore); err == nil {
			t.Fatal("restore cleanup over quarantine residue succeeded")
		}
	})
}

func createApplyPatchTxnEffectFinalRestoreStage(
	t *testing.T,
	operation *applyPatchTxnIntent,
	restore *applyPatchTransactionJournalArtifact,
	data []byte,
) {
	t.Helper()
	file, identity, err := applyPatchTxnCreateRegular(
		operation.source.anchor, restore.Rooted.Basename, 0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
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
	restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	restore.Rooted.Links = 1
}
