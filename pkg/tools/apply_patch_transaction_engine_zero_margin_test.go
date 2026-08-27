package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEngineZeroMarginCapabilities(t *testing.T) {
	t.Run("source anchor unavailable", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		if err := operation.source.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		if err := probeApplyPatchTxnNoReplaceCapabilities(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("capability probe accepted a closed source anchor")
		}
	})

	t.Run("forest checkpoint missing", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginForest(t)
		transaction.journal.Forests[0].StageRoot.Identity = nil
		if err := probeApplyPatchTxnNoReplaceCapabilities(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("capability probe accepted an uncheckpointed forest")
		}
	})

	t.Run("forest anchor unavailable", func(t *testing.T) {
		_, transaction, forestIntent := newApplyPatchTxnZeroMarginForest(t)
		if err := forestIntent.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		if err := probeApplyPatchTxnNoReplaceCapabilities(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("capability probe accepted a closed forest anchor")
		}
	})
}

func TestApplyPatchTransactionEngineZeroMarginPublicState(t *testing.T) {
	t.Run("source artifact missing", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginMove(t)
		removeApplyPatchTxnExactArtifact(
			transaction.journal,
			applyPatchTransactionArtifactSourceProbeWitness,
		)
		if err := verifyApplyPatchTxnPreparingPublicState(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("public-state verification accepted a missing source artifact")
		}
	})

	t.Run("source anchor unavailable", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		if err := operation.source.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnPreparingPublicState(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("public-state verification accepted a closed source anchor")
		}
	})

	for _, state := range []string{
		"target anchor unavailable",
		"stage checkpoint missing",
		"witness checkpoint missing",
		"witness content conflict",
		"rollback artifact missing",
	} {
		t.Run(state, func(t *testing.T) {
			_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
			switch state {
			case "target anchor unavailable":
				if err := operation.targetAnchor.Close(); err != nil {
					t.Fatal(err)
				}
			case "stage checkpoint missing":
				stage, err := requireApplyPatchTxnArtifact(
					transaction.journal,
					operation.index,
					applyPatchTransactionArtifactPostimageStage,
				)
				if err != nil {
					t.Fatal(err)
				}
				stage.Rooted.Identity = nil
			case "witness checkpoint missing", "witness content conflict":
				witness, err := requireApplyPatchTxnArtifact(
					transaction.journal,
					operation.index,
					applyPatchTransactionArtifactPostimageWitness,
				)
				if err != nil {
					t.Fatal(err)
				}
				if state == "witness checkpoint missing" {
					witness.Rooted.Identity = nil
				} else {
					witness.Expected.SHA256 = "changed"
				}
			case "rollback artifact missing":
				removeApplyPatchTxnExactArtifact(
					transaction.journal,
					applyPatchTransactionArtifactTargetRollbackQuarantine,
				)
			}
			if err := verifyApplyPatchTxnPreparingPublicState(
				transaction.intent,
				transaction.journal,
			); err == nil {
				t.Fatal("public-state verification accepted an invalid target state")
			}
		})
	}

	t.Run("forest journal missing", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginForest(t)
		transaction.journal.Forests = nil
		if err := verifyApplyPatchTxnPreparingPublicState(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("public-state verification accepted a missing forest journal")
		}
	})

	t.Run("forest checkpoint missing", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginForest(t)
		transaction.journal.Forests[0].StageRoot.Identity = nil
		if err := verifyApplyPatchTxnPreparingPublicState(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("public-state verification accepted an uncheckpointed forest")
		}
	})
}

func TestApplyPatchTransactionEngineZeroMarginStagedForest(t *testing.T) {
	for _, state := range []string{
		"anchor unavailable",
		"entry parent missing",
		"directory identity conflict",
		"witness identity conflict",
	} {
		t.Run(state, func(t *testing.T) {
			_, transaction, forestIntent := newApplyPatchTxnZeroMarginForest(t)
			forest := &transaction.journal.Forests[0]
			switch state {
			case "anchor unavailable":
				if err := forestIntent.anchor.Close(); err != nil {
					t.Fatal(err)
				}
			case "entry parent missing":
				entry := &forest.Entries[len(forest.Entries)-1]
				entry.RelativePath = filepath.ToSlash(filepath.Join("missing", filepath.Base(entry.RelativePath)))
			case "directory identity conflict":
				for index := 1; index < len(forest.Entries); index++ {
					if forest.Entries[index].Kind == "directory" {
						forest.Entries[index].Identity.File++
						break
					}
				}
			case "witness identity conflict":
				forest.SentinelWitness.Identity.File++
			}
			if err := verifyApplyPatchTxnStagedForest(forestIntent, forest); err == nil {
				t.Fatal("staged-forest verification accepted an invalid forest")
			}
		})
	}
}

func TestApplyPatchTransactionEngineZeroMarginForestManifest(t *testing.T) {
	t.Run("root missing", func(t *testing.T) {
		_, transaction, forestIntent := newApplyPatchTxnZeroMarginForest(t)
		if err := verifyApplyPatchTxnForestManifestAt(
			forestIntent,
			&transaction.journal.Forests[0],
			"missing-root",
		); err == nil {
			t.Fatal("forest manifest accepted a missing root")
		}
	})

	t.Run("name mismatch", func(t *testing.T) {
		_, transaction, forestIntent := newApplyPatchTxnZeroMarginForest(t)
		forest := cloneApplyPatchTransactionJournal(t, transaction.journal).Forests[0]
		entry := &forest.Entries[len(forest.Entries)-1]
		entry.RelativePath = filepath.ToSlash(filepath.Join(
			filepath.Dir(filepath.FromSlash(entry.RelativePath)),
			"different-name",
		))
		if err := verifyApplyPatchTxnForestManifestAt(
			forestIntent,
			&forest,
			forestIntent.stageRoot,
		); err == nil {
			t.Fatal("forest manifest accepted a mismatched entry name")
		}
	})

	t.Run("directory unavailable", func(t *testing.T) {
		_, transaction, forestIntent := newApplyPatchTxnZeroMarginForest(t)
		forest := &transaction.journal.Forests[0]
		for _, entry := range forest.Entries {
			if entry.Kind == "directory" && entry.RelativePath != "." {
				path := filepath.Join(
					forestIntent.anchorPath,
					forestIntent.stageRoot,
					filepath.FromSlash(entry.RelativePath),
				)
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		if err := verifyApplyPatchTxnForestManifestAt(
			forestIntent,
			forest,
			forestIntent.stageRoot,
		); err == nil {
			t.Fatal("forest manifest accepted a missing directory")
		}
	})
}
