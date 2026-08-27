package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func cleanupApplyPatchTxnPrePONR(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	store *applyPatchTxnStore,
	key []byte,
) error {
	if intent == nil || journal == nil || store == nil {
		return errors.New("apply-patch transaction cleanup state is unavailable")
	}
	if err := store.preparePrivateCleanup(key, journal); err != nil {
		return fmt.Errorf("apply-patch transaction private cleanup conflict: %w", err)
	}
	checkpoint := func() error { return store.writeJournal(key, journal) }
	if err := cleanupApplyPatchTxnPublicStages(intent, journal, checkpoint); err != nil {
		return fmt.Errorf("apply-patch transaction private cleanup conflict: %w", err)
	}
	if err := store.finishCommittedStateCleanup(); err != nil {
		return fmt.Errorf("apply-patch transaction private cleanup conflict: %w", err)
	}
	return nil
}

func cleanupApplyPatchTxnPublicStages(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	checkpoint func() error,
) error {
	for index := len(intent.operations) - 1; index >= 0; index-- {
		operation := intent.operations[index]
		if operation == nil || operation.source == nil {
			continue
		}
		for _, role := range []applyPatchTransactionArtifactRole{
			applyPatchTransactionArtifactSourceProbeWitness,
			applyPatchTransactionArtifactSourceRestoreStage,
		} {
			artifact, err := requireApplyPatchTxnArtifact(
				journal,
				operation.index,
				role,
			)
			if err != nil {
				return err
			}
			if artifact.Rooted.Identity == nil {
				if err := requireApplyPatchTxnRootedAbsent(
					operation.source.anchor,
					artifact.Rooted,
				); err != nil {
					return err
				}
				continue
			}
			if err := removeApplyPatchTxnRootedWithCheckpoint(
				operation.source.anchor,
				artifact.Rooted,
				false,
				checkpoint,
			); err != nil {
				return err
			}
		}
	}
	for index := len(intent.forests) - 1; index >= 0; index-- {
		forestIntent := intent.forests[index]
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil {
			return err
		}
		if err := cleanupApplyPatchTxnForestStage(
			forestIntent,
			forest,
			checkpoint,
		); err != nil {
			return err
		}
	}
	for index := len(intent.operations) - 1; index >= 0; index-- {
		operation := intent.operations[index]
		if operation == nil || operation.targetAnchor == nil || operation.forest != nil {
			continue
		}
		witness, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil {
			return err
		}
		stage, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			return err
		}
		if witness.Rooted.Identity != nil {
			if err := removeApplyPatchTxnRootedWithCheckpoint(
				operation.targetAnchor,
				witness.Rooted,
				false,
				checkpoint,
			); err != nil {
				return err
			}
			if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
				return err
			}
		}
		if stage.Rooted.Identity != nil {
			if err := removeApplyPatchTxnRootedWithCheckpoint(
				operation.targetAnchor,
				stage.Rooted,
				false,
				checkpoint,
			); err != nil {
				return err
			}
			if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupApplyPatchTxnForestStage(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	checkpoint func() error,
) error {
	if intent == nil || forest == nil || intent.anchor == nil {
		return errors.New("apply-patch transaction forest cleanup state is unavailable")
	}
	if forest.SentinelWitness.Identity != nil {
		if err := removeApplyPatchTxnRootedWithCheckpoint(
			intent.anchor,
			&forest.SentinelWitness,
			false,
			checkpoint,
		); err != nil {
			return err
		}
		if err := applyPatchTxnSyncDirectory(intent.anchor); err != nil {
			return err
		}
	}
	if forest.StageRoot.Identity == nil {
		return nil
	}
	stageRootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
	stageRoot, err := openApplyPatchTxnAnchor(stageRootPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !stageRoot.identity.equal(*forest.StageRoot.Identity) {
		_ = stageRoot.Close()
		return errors.New("apply-patch transaction forest root changed before cleanup")
	}
	for entryIndex := len(forest.Entries) - 1; entryIndex >= 1; entryIndex-- {
		entry := &forest.Entries[entryIndex]
		if entry.Identity == nil {
			continue
		}
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		parentPath := stageRootPath
		if parentRelative != "." {
			parentPath = filepath.Join(stageRootPath, parentRelative)
		}
		parent, openErr := openApplyPatchTxnAnchor(parentPath)
		if openErr != nil {
			_ = stageRoot.Close()
			return openErr
		}
		basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
		removeErr := removeApplyPatchTxnForestEntryWithCheckpoint(
			parent,
			basename,
			entry,
			checkpoint,
		)
		if removeErr == nil {
			removeErr = applyPatchTxnSyncDirectory(parent)
		}
		closeErr := parent.Close()
		if removeErr != nil || closeErr != nil {
			_ = stageRoot.Close()
			return errors.Join(removeErr, closeErr)
		}
	}
	if err := stageRoot.Close(); err != nil {
		return err
	}
	if err := removeApplyPatchTxnRootedWithCheckpoint(
		intent.anchor,
		&forest.StageRoot,
		true,
		checkpoint,
	); err != nil {
		return err
	}
	if err := applyPatchTxnSyncDirectory(intent.anchor); err != nil {
		return err
	}
	return nil
}
