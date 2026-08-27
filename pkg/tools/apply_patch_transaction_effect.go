package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	errApplyPatchRollbackIncomplete = errors.New("apply-patch rollback incomplete")
	errApplyPatchCommitUncertain    = errors.New("apply-patch commit outcome uncertain")
)

type applyPatchTxnEffects struct {
	sourceQuarantined         map[int]bool
	sourceRestoreRequired     map[int]bool
	targetPublished           map[int]bool
	targetRollbackQuarantined map[int]bool
	forestPublished           map[string]bool
	forestRollbackQuarantined map[string]bool
}

func (transaction *applyPatchPreparedTransaction) commit() error {
	if transaction == nil || transaction.closed || transaction.journal == nil ||
		transaction.store == nil || transaction.intent == nil ||
		transaction.journal.Phase != applyPatchTransactionPhasePrepared {
		return errors.New("apply-patch prepared transaction is unavailable")
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := validateApplyPatchTxnPreEffectDeclaredNames(
		transaction.intent,
		transaction.journal,
	); err != nil {
		return transaction.rollback(err)
	}
	if err := validateApplyPatchTxnRecoveryParticipants(transaction); err != nil {
		return transaction.rollback(err)
	}
	if err := transaction.quarantineSources(); err != nil {
		return transaction.rollback(err)
	}
	if err := transaction.publishTargets(); err != nil {
		return transaction.rollback(err)
	}
	if err := verifyApplyPatchTxnCommittedPublicState(
		transaction.intent,
		transaction.journal,
		transaction.effects,
	); err != nil {
		return transaction.rollback(err)
	}
	if err := transaction.injectFault("before_decision"); err != nil {
		return transaction.rollback(err)
	}
	if err := verifyApplyPatchTxnCommittedPublicState(
		transaction.intent,
		transaction.journal,
		transaction.effects,
	); err != nil {
		return transaction.rollback(err)
	}
	transaction.journal.DecisionAttempted = true
	if err := transaction.persistForwardDecisionState(); err != nil {
		if errors.Is(err, errApplyPatchCommitUncertain) {
			_ = transaction.closeHandles()
			return err
		}
		return transaction.rollback(err)
	}
	transaction.journal.Phase = applyPatchTransactionPhaseCommitted
	if err := transaction.persistCommittedDecision(); err != nil {
		if errors.Is(err, errApplyPatchCommitUncertain) {
			_ = transaction.closeHandles()
			return err
		}
		return transaction.rollback(err)
	}
	// A committed cleanup failure is retryable housekeeping. The postimage is
	// already the durable decision and must not be reported as an ordinary
	// failed patch or rolled back.
	cleanupErr := transaction.cleanupCommitted()
	closeErr := transaction.closeHandles()
	if cleanupErr != nil || closeErr != nil {
		return committedApplyPatchCleanupDeferred(errors.Join(cleanupErr, closeErr))
	}
	return nil
}

func committedApplyPatchCleanupDeferred(error) error {
	// The durable committed postimage is the successful tool outcome. Recovery
	// retries retained private housekeeping on the next lock acquisition.
	return nil
}

func (transaction *applyPatchPreparedTransaction) quarantineSources() error {
	for _, operation := range transaction.intent.operations {
		if operation.source == nil {
			continue
		}
		journalOperation := &transaction.journal.Operations[operation.index]
		expected := *journalOperation.Source.PreflightIdentity
		witness, artifactErr := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceWitness,
		)
		if artifactErr != nil {
			return artifactErr
		}
		linkErr := applyPatchTxnLinkWitness(
			operation.source.anchor,
			operation.source.basename,
			expected,
			2,
			operation.source.anchor,
			witness.Rooted.Basename,
			witness.Rooted.RemovalBasename,
		)
		if linkErr != nil {
			return linkErr
		}
		witness.Rooted.Identity = copyApplyPatchTxnIdentity(expected)
		witness.Rooted.Links = 2
		before := journalOperation.Before
		sourceState, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			operation.source.basename,
			before,
			2,
		)
		if verifyErr != nil || !sourceState.Identity.equal(expected) {
			return errors.Join(
				errors.New("apply-patch source changed after witness creation"),
				verifyErr,
			)
		}
		witnessState, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			witness.Rooted.Basename,
			before,
			2,
		)
		if verifyErr != nil || !witnessState.Identity.equal(expected) {
			return errors.Join(
				errors.New("apply-patch source witness changed after creation"),
				verifyErr,
			)
		}
		syncErr := applyPatchTxnSyncDirectory(operation.source.anchor)
		if syncErr != nil {
			return syncErr
		}
		checkpointErr := transaction.checkpoint()
		if checkpointErr != nil {
			return checkpointErr
		}
		faultErr := transaction.injectFault(
			fmt.Sprintf("source_witness:%d", operation.index),
		)
		if faultErr != nil {
			return faultErr
		}
		quarantine, artifactErr := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if artifactErr != nil {
			return artifactErr
		}
		quarantineErr := applyPatchTxnQuarantineExact(
			operation.source.anchor,
			operation.source.basename,
			quarantine.Rooted.Basename,
			expected,
		)
		if quarantineErr != nil {
			return quarantineErr
		}
		quarantine.Rooted.Identity = copyApplyPatchTxnIdentity(expected)
		quarantine.Rooted.Links = 2
		transaction.effects.sourceQuarantined[operation.index] = true
		if verifyErr := verifyApplyPatchTxnRetainedOriginal(
			operation,
			transaction.journal,
		); verifyErr != nil {
			return verifyErr
		}
		syncErr = applyPatchTxnSyncDirectory(operation.source.anchor)
		if syncErr != nil {
			return syncErr
		}
		checkpointErr = transaction.checkpoint()
		if checkpointErr != nil {
			return checkpointErr
		}
		faultErr = transaction.injectFault(
			fmt.Sprintf("source_quarantine:%d", operation.index),
		)
		if faultErr != nil {
			return faultErr
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) publishTargets() error {
	publishedForests := make(map[string]struct{}, len(transaction.intent.forests))
	for _, operation := range transaction.intent.operations {
		if operation.planned.targetPath == "" {
			continue
		}
		if operation.forest != nil {
			forest := operation.forest
			if _, published := publishedForests[forest.id]; published {
				continue
			}
			journalForest, err := requireApplyPatchTxnJournalForest(
				transaction.journal,
				forest.id,
			)
			if err != nil {
				return err
			}
			if err := verifyApplyPatchTxnStagedForest(forest, journalForest); err != nil {
				return err
			}
			if err := applyPatchTxnRenameNoReplace(
				forest.anchor,
				forest.stageRoot,
				forest.anchor,
				forest.publicRoot,
			); err != nil {
				return err
			}
			transaction.effects.forestPublished[forest.id] = true
			publishedForests[forest.id] = struct{}{}
			if err := applyPatchTxnSyncDirectory(forest.anchor); err != nil {
				return err
			}
			if err := transaction.checkpoint(); err != nil {
				return err
			}
			if err := transaction.injectFault("forest_publish:" + forest.id); err != nil {
				return err
			}
			continue
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err != nil {
			return err
		}
		if err := applyPatchTxnRenameNoReplace(
			operation.targetAnchor,
			operation.stageName,
			operation.targetAnchor,
			operation.targetLayout.components[0],
		); err != nil {
			return err
		}
		transaction.effects.targetPublished[operation.index] = true
		if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
			return err
		}
		if err := transaction.checkpoint(); err != nil {
			return err
		}
		if err := transaction.injectFault(
			fmt.Sprintf("target_publish:%d", operation.index),
		); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) injectFault(boundary string) error {
	if transaction == nil || transaction.fault == nil {
		return nil
	}
	return transaction.fault(boundary)
}

func (transaction *applyPatchPreparedTransaction) checkpoint() error {
	return transaction.store.writeJournal(
		transaction.key[:],
		transaction.journal,
		transaction.fault,
	)
}

func (transaction *applyPatchPreparedTransaction) persistForwardDecisionState() error {
	writeErr := transaction.checkpoint()
	if writeErr == nil {
		return nil
	}
	persisted, _, readErr := transaction.store.readJournal(transaction.key[:])
	if readErr != nil || persisted.TransactionID != transaction.journal.TransactionID {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, readErr)
	}
	if persisted.Phase != applyPatchTransactionPhasePrepared ||
		!persisted.DecisionAttempted {
		return writeErr
	}
	return transaction.syncJournalDirectoryOrUncertain(writeErr)
}

func (transaction *applyPatchPreparedTransaction) persistCommittedDecision() error {
	writeErr := transaction.checkpoint()
	if writeErr == nil {
		return nil
	}
	persisted, _, readErr := transaction.store.readJournal(transaction.key[:])
	if readErr != nil || persisted.TransactionID != transaction.journal.TransactionID {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, readErr)
	}
	if persisted.Phase == applyPatchTransactionPhaseCommitted &&
		persisted.DecisionAttempted {
		return transaction.syncJournalDirectoryOrUncertain(writeErr)
	}
	if persisted.Phase == applyPatchTransactionPhasePrepared &&
		persisted.DecisionAttempted {
		transaction.journal.Phase = applyPatchTransactionPhasePrepared
		return writeErr
	}
	return errors.Join(errApplyPatchCommitUncertain, writeErr)
}

func (transaction *applyPatchPreparedTransaction) syncJournalDirectoryOrUncertain(
	writeErr error,
) error {
	transaction.store.mu.Lock()
	defer transaction.store.mu.Unlock()
	if err := transaction.store.revalidateLocked(); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	if err := transaction.store.revalidateCurrentJournalLocked(
		transaction.key[:],
	); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	if err := transaction.injectFault("journal_decision_resync"); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	// The deterministic seam may model an external namespace change. Repeat
	// both named-directory and exact journal validation before syncing so an
	// open handle to a moved directory cannot falsely prove the visible marker.
	if err := transaction.store.revalidateLocked(); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	if err := transaction.store.revalidateCurrentJournalLocked(
		transaction.key[:],
	); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	if err := syncApplyPatchTxnRootDirectory(transaction.store.activeRoot); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, writeErr, err)
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) rollback(primary error) error {
	if err := validateApplyPatchTxnRecoveryParticipants(transaction); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	transaction.journal.Phase = applyPatchTransactionPhaseRollingBack
	if err := transaction.checkpoint(); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	if err := transaction.restoreMoveSources(); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	for index := len(transaction.intent.operations) - 1; index >= 0; index-- {
		operation := transaction.intent.operations[index]
		if operation.forest != nil {
			continue
		}
		if transaction.effects.targetRollbackQuarantined[operation.index] {
			if err := transaction.finishRollbackQuarantinedTarget(operation); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		} else if transaction.effects.targetPublished[operation.index] {
			if err := transaction.rollbackPublishedTarget(operation); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		} else if operation.targetAnchor != nil {
			if err := cleanupApplyPatchTxnUnpublishedTarget(
				operation,
				transaction.journal,
				transaction.checkpoint,
			); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		}
		if operation.planned.kind != "move" &&
			(transaction.effects.sourceQuarantined[operation.index] ||
				transaction.effects.sourceRestoreRequired[operation.index]) {
			if err := transaction.restoreSource(operation); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		}
	}
	for index := len(transaction.intent.forests) - 1; index >= 0; index-- {
		forest := transaction.intent.forests[index]
		journalForest, err := requireApplyPatchTxnJournalForest(
			transaction.journal,
			forest.id,
		)
		if err != nil {
			_ = transaction.closeHandles()
			return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
		}
		if transaction.effects.forestRollbackQuarantined[forest.id] {
			if err := removeApplyPatchTxnForestTree(
				forest,
				journalForest,
				forest.rollbackRoot,
				transaction.checkpoint,
			); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		} else if transaction.effects.forestPublished[forest.id] {
			if err := transaction.rollbackPublishedForest(forest, journalForest); err != nil {
				_ = transaction.closeHandles()
				return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
			}
		} else if err := cleanupApplyPatchTxnForestStage(
			forest,
			journalForest,
			transaction.checkpoint,
		); err != nil {
			_ = transaction.closeHandles()
			return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
		}
	}
	if err := verifyApplyPatchTxnRolledBackPublicState(
		transaction.intent,
		transaction.journal,
		true,
	); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	if err := transaction.cleanupRollbackSourceWitnesses(); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	if err := verifyApplyPatchTxnRollbackPrivateResidueAbsent(
		transaction.intent,
		transaction.journal,
	); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	if err := verifyApplyPatchTxnRolledBackPublicState(
		transaction.intent,
		transaction.journal,
		false,
	); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	if err := transaction.store.cleanupOwnedStateAuthenticated(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		_ = transaction.closeHandles()
		return errors.Join(primary, errApplyPatchRollbackIncomplete, err)
	}
	closeErr := transaction.closeHandles()
	return errors.Join(primary, closeErr)
}

func verifyApplyPatchTxnRollbackPrivateResidueAbsent(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) error {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Rooted == nil {
			continue
		}
		anchor, err := openApplyPatchTxnAnchor(artifact.Rooted.AnchorCanonicalPath)
		if err != nil || !anchor.identity.equal(artifact.Rooted.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return errors.Join(
				errors.New("apply-patch rollback artifact anchor changed"),
				err,
			)
		}
		for _, name := range []string{
			artifact.Rooted.Basename,
			artifact.Rooted.RemovalBasename,
		} {
			if err := requireApplyPatchTxnAbsent(anchor, name); err != nil {
				_ = anchor.Close()
				return errors.New("apply-patch rollback private artifact residue conflict")
			}
		}
		if err := anchor.Close(); err != nil {
			return err
		}
	}
	for _, forestIntent := range intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil {
			return err
		}
		for _, location := range []*applyPatchTransactionJournalRootedLocation{
			&forest.StageRoot,
			&forest.RollbackRoot,
			&forest.SentinelWitness,
		} {
			for _, name := range []string{location.Basename, location.RemovalBasename} {
				if err := requireApplyPatchTxnAbsent(forestIntent.anchor, name); err != nil {
					return errors.New("apply-patch rollback forest residue conflict")
				}
			}
		}
	}
	return nil
}

func verifyApplyPatchTxnRolledBackPublicState(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	withWitnesses bool,
) error {
	for _, operation := range intent.operations {
		journalOperation := &journal.Operations[operation.index]
		if operation.source != nil {
			restore, restoreErr := requireApplyPatchTxnArtifact(
				journal,
				operation.index,
				applyPatchTransactionArtifactSourceRestoreStage,
			)
			witness, witnessErr := requireApplyPatchTxnArtifact(
				journal,
				operation.index,
				applyPatchTransactionArtifactSourceWitness,
			)
			if restoreErr != nil || witnessErr != nil {
				return errors.Join(restoreErr, witnessErr)
			}
			expectedIdentity := operation.source.state.Identity
			expectedLinks := uint64(1)
			if restore.Rooted.Identity != nil {
				expectedIdentity = *restore.Rooted.Identity
				if withWitnesses && restore.Rooted.Links > 1 {
					expectedLinks = 2
				}
			} else if withWitnesses && witness.Rooted.Identity != nil {
				present, presentErr := applyPatchTxnRecoveryIdentityPresent(
					operation.source.anchor,
					witness.Rooted.Basename,
					*witness.Rooted.Identity,
				)
				if presentErr != nil {
					return presentErr
				}
				if present {
					expectedLinks = 2
				}
			}
			state, err := verifyApplyPatchTxnRegular(
				operation.source.anchor,
				operation.source.basename,
				journalOperation.Before,
				expectedLinks,
			)
			if err != nil || !state.Identity.equal(expectedIdentity) {
				return errors.Join(
					errors.New("apply-patch rollback preimage source changed"),
					err,
				)
			}
			if withWitnesses && restore.Rooted.Identity != nil &&
				restore.Rooted.Links > 1 {
				stageState, stageErr := verifyApplyPatchTxnRegular(
					operation.source.anchor,
					restore.Rooted.Basename,
					journalOperation.Before,
					2,
				)
				if stageErr != nil || !stageState.Identity.equal(state.Identity) {
					return errors.Join(
						errors.New("apply-patch rollback restore witness changed"),
						stageErr,
					)
				}
			}
		}
		if operation.planned.targetPath != "" && operation.planned.kind != "update" {
			layout := operation.targetLayout
			if operation.forest == nil {
				if err := requireApplyPatchTxnAbsent(
					operation.targetAnchor,
					layout.components[0],
				); err != nil {
					return err
				}
			}
		}
	}
	for _, forest := range intent.forests {
		if err := requireApplyPatchTxnAbsent(forest.anchor, forest.publicRoot); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) cleanupRollbackSourceWitnesses() error {
	for _, operation := range transaction.intent.operations {
		if operation.source == nil {
			continue
		}
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceWitness,
		)
		if err != nil {
			return err
		}
		restore, restoreErr := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceRestoreStage,
		)
		if restoreErr != nil {
			return restoreErr
		}
		for _, rooted := range []*applyPatchTransactionJournalRootedLocation{
			witness.Rooted,
			restore.Rooted,
		} {
			if rooted == nil || rooted.Identity == nil {
				continue
			}
			if err := transaction.removeRooted(
				operation.source.anchor,
				rooted,
				false,
			); err != nil {
				return err
			}
			if rooted == restore.Rooted {
				restore.Rooted.Links = 1
				if err := transaction.checkpoint(); err != nil {
					return err
				}
				if faultErr := transaction.injectFault(fmt.Sprintf(
					"restore_private_witness_removed:%d",
					operation.index,
				)); faultErr != nil {
					return faultErr
				}
			} else if faultErr := transaction.injectFault(fmt.Sprintf(
				"restore_old_witness_removed:%d",
				operation.index,
			)); faultErr != nil {
				return faultErr
			}
			if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
				return err
			}
			if err := transaction.checkpoint(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) restoreMoveSources() error {
	for index := len(transaction.intent.operations) - 1; index >= 0; index-- {
		operation := transaction.intent.operations[index]
		if operation.planned.kind == "move" &&
			(transaction.effects.sourceQuarantined[operation.index] ||
				transaction.effects.sourceRestoreRequired[operation.index]) {
			if err := transaction.restoreSource(operation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) restoreSource(
	operation *applyPatchTxnIntent,
) error {
	if transaction.effects.sourceRestoreRequired == nil {
		transaction.effects.sourceRestoreRequired = make(map[int]bool)
	}
	quarantine, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if artifactErr != nil {
		return artifactErr
	}
	quarantinePresent := false
	if quarantine.Rooted.Identity != nil {
		quarantinePresent, artifactErr = applyPatchTxnRecoveryIdentityPresent(
			operation.source.anchor,
			quarantine.Rooted.Basename,
			*quarantine.Rooted.Identity,
		)
		if artifactErr != nil {
			return artifactErr
		}
	}
	if !quarantinePresent {
		return transaction.restoreSourceFromBackupV2(operation)
	}
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if artifactErr != nil || witness.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch original witness is unavailable"),
			artifactErr,
		)
	}
	before := transaction.journal.Operations[operation.index].Before
	quarantineState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		before,
		quarantine.Rooted.Links,
	)
	if verifyErr != nil || !quarantineState.Identity.equal(*quarantine.Rooted.Identity) ||
		!quarantineState.Identity.equal(*witness.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch original quarantine changed before restore"),
			verifyErr,
		)
	}
	witnessState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		witness.Rooted.Basename,
		before,
		witness.Rooted.Links,
	)
	if verifyErr != nil || !witnessState.Identity.equal(quarantineState.Identity) {
		return errors.Join(
			errors.New("apply-patch original witness changed before restore"),
			verifyErr,
		)
	}
	renameErr := applyPatchTxnRenameNoReplace(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		operation.source.anchor,
		operation.source.basename,
	)
	if renameErr != nil {
		return renameErr
	}
	restoredState, restoredErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		operation.source.basename,
		before,
		2,
	)
	if restoredErr != nil || !restoredState.Identity.equal(*witness.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch restored source changed after publication"),
			restoredErr,
		)
	}
	transaction.effects.sourceQuarantined[operation.index] = false
	transaction.effects.sourceRestoreRequired[operation.index] = false
	syncErr := applyPatchTxnSyncDirectory(operation.source.anchor)
	if syncErr != nil {
		return syncErr
	}
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		return checkpointErr
	}
	return nil
}

func verifyApplyPatchTxnBackupFallbackWitness(
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
) error {
	witness, err := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if err != nil || witness.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch backup fallback witness is unavailable"),
			err,
		)
	}
	state, err := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		witness.Rooted.Basename,
		journal.Operations[operation.index].Before,
		1,
	)
	if err != nil || !state.Identity.equal(*witness.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch backup fallback witness changed"),
			err,
		)
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) restoreSourceFromBackupV2(
	operation *applyPatchTxnIntent,
) error {
	if transaction.effects.sourceRestoreRequired == nil {
		transaction.effects.sourceRestoreRequired = make(map[int]bool)
	}
	before := transaction.journal.Operations[operation.index].Before
	backup, err := transaction.store.readBackup(
		transaction.key[:],
		transaction.journal,
		operation.index,
	)
	if err != nil {
		return err
	}
	restore, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceRestoreStage,
	)
	if err != nil {
		return err
	}
	if restore.Rooted.Identity != nil {
		publicState, publicErr := applyPatchTxnInspectAt(
			operation.source.anchor,
			operation.source.basename,
		)
		if publicErr == nil {
			if !publicState.Identity.equal(*restore.Rooted.Identity) {
				return errors.New("apply-patch backup restore target identity conflict")
			}
			stagePresent, stageErr := applyPatchTxnRecoveryIdentityPresent(
				operation.source.anchor,
				restore.Rooted.Basename,
				*restore.Rooted.Identity,
			)
			if stageErr != nil {
				return stageErr
			}
			oldWitness, witnessErr := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactSourceWitness,
			)
			if witnessErr != nil || oldWitness.Rooted.Identity == nil {
				return errors.Join(
					errors.New("apply-patch backup fallback witness metadata is unavailable"),
					witnessErr,
				)
			}
			oldWitnessPresent, witnessErr := applyPatchTxnRecoveryIdentityPresent(
				operation.source.anchor,
				oldWitness.Rooted.Basename,
				*oldWitness.Rooted.Identity,
			)
			if witnessErr != nil {
				return witnessErr
			}
			if !stagePresent && oldWitnessPresent {
				return errors.New(
					"apply-patch backup restore cleanup order is ambiguous",
				)
			}
			expectedLinks := uint64(1)
			if stagePresent {
				expectedLinks = 2
			}
			verified, verifyErr := verifyApplyPatchTxnRegular(
				operation.source.anchor,
				operation.source.basename,
				before,
				expectedLinks,
			)
			if verifyErr != nil || !verified.Identity.equal(*restore.Rooted.Identity) {
				return errors.Join(
					errors.New("apply-patch backup-restored source changed"),
					verifyErr,
				)
			}
			if stagePresent {
				stageState, stageVerifyErr := verifyApplyPatchTxnRegular(
					operation.source.anchor,
					restore.Rooted.Basename,
					before,
					2,
				)
				if stageVerifyErr != nil || !stageState.Identity.equal(verified.Identity) {
					return errors.Join(
						errors.New("apply-patch backup restore witness changed"),
						stageVerifyErr,
					)
				}
				if syncErr := applyPatchTxnSyncDirectory(operation.source.anchor); syncErr != nil {
					return syncErr
				}
				if restore.Rooted.Links != 2 {
					restore.Rooted.Links = 2
					if checkpointErr := transaction.checkpoint(); checkpointErr != nil {
						return checkpointErr
					}
				}
			} else {
				restore.Rooted.Links = 1
				if syncErr := applyPatchTxnSyncDirectory(operation.source.anchor); syncErr != nil {
					return syncErr
				}
			}
			return transaction.finishBackupRestoredSourceCleanup(operation, restore)
		}
		if !errors.Is(publicErr, os.ErrNotExist) {
			return publicErr
		}
		witnessValidationErr := verifyApplyPatchTxnBackupFallbackWitness(
			operation,
			transaction.journal,
		)
		if witnessValidationErr != nil {
			return witnessValidationErr
		}
		stageState, stageErr := applyPatchTxnInspectAt(
			operation.source.anchor,
			restore.Rooted.Basename,
		)
		if stageErr != nil || !stageState.Identity.equal(*restore.Rooted.Identity) ||
			stageState.Links != 1 || stageState.Size < 0 ||
			uint64(stageState.Size) > before.Length {
			return errors.Join(
				errors.New("apply-patch backup restore stage changed before resume"),
				stageErr,
			)
		}
		resumeErr := applyPatchTxnResumeRegularContext(
			context.Background(),
			operation.source.anchor,
			restore.Rooted.Basename,
			*restore.Rooted.Identity,
			backup,
			os.FileMode(before.Mode),
		)
		if resumeErr != nil {
			return resumeErr
		}
	} else {
		witnessValidationErr := verifyApplyPatchTxnBackupFallbackWitness(
			operation,
			transaction.journal,
		)
		if witnessValidationErr != nil {
			return witnessValidationErr
		}
		absenceErr := requireApplyPatchTxnAbsent(
			operation.source.anchor,
			operation.source.basename,
		)
		if absenceErr != nil {
			return absenceErr
		}
		file, identity, createErr := applyPatchTxnCreateRegular(
			operation.source.anchor,
			restore.Rooted.Basename,
			0o600,
		)
		if createErr != nil {
			return createErr
		}
		if faultErr := transaction.injectFault(fmt.Sprintf(
			"restore_create_before_identity:%d",
			operation.index,
		)); faultErr != nil {
			_ = file.Close()
			return faultErr
		}
		restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
		restore.Rooted.Links = 1
		if syncErr := applyPatchTxnSyncDirectory(operation.source.anchor); syncErr != nil {
			_ = file.Close()
			return syncErr
		}
		if checkpointErr := transaction.checkpoint(); checkpointErr != nil {
			_ = file.Close()
			return checkpointErr
		}
		if faultErr := transaction.injectFault(fmt.Sprintf(
			"restore_identity_checkpoint:%d",
			operation.index,
		)); faultErr != nil {
			_ = file.Close()
			return faultErr
		}
		writeErr := applyPatchTxnWriteRegularContext(
			context.Background(),
			file,
			backup,
			os.FileMode(before.Mode),
			true,
		)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
	}
	if syncErr := applyPatchTxnSyncDirectory(operation.source.anchor); syncErr != nil {
		return syncErr
	}
	if faultErr := transaction.injectFault(fmt.Sprintf(
		"restore_stage_synced:%d",
		operation.index,
	)); faultErr != nil {
		return faultErr
	}
	stageState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		restore.Rooted.Basename,
		before,
		1,
	)
	if verifyErr != nil || restore.Rooted.Identity == nil ||
		!stageState.Identity.equal(*restore.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch backup restore stage changed"),
			verifyErr,
		)
	}
	witnessValidationErr := verifyApplyPatchTxnBackupFallbackWitness(
		operation,
		transaction.journal,
	)
	if witnessValidationErr != nil {
		return witnessValidationErr
	}
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil {
		return err
	}
	quarantineAbsenceErr := requireApplyPatchTxnRootedAbsent(
		operation.source.anchor,
		quarantine.Rooted,
	)
	if quarantineAbsenceErr != nil {
		return quarantineAbsenceErr
	}
	if linkErr := applyPatchTxnLinkWitness(
		operation.source.anchor,
		restore.Rooted.Basename,
		*restore.Rooted.Identity,
		2,
		operation.source.anchor,
		operation.source.basename,
		restore.Rooted.RemovalBasename,
	); linkErr != nil {
		return linkErr
	}
	if faultErr := transaction.injectFault(fmt.Sprintf(
		"restore_link_published_before_checkpoint:%d",
		operation.index,
	)); faultErr != nil {
		return faultErr
	}
	restore.Rooted.Links = 2
	if syncErr := applyPatchTxnSyncDirectory(operation.source.anchor); syncErr != nil {
		return syncErr
	}
	if checkpointErr := transaction.checkpoint(); checkpointErr != nil {
		return checkpointErr
	}
	if faultErr := transaction.injectFault(fmt.Sprintf(
		"restore_published:%d",
		operation.index,
	)); faultErr != nil {
		return faultErr
	}
	publicState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		operation.source.basename,
		before,
		2,
	)
	if verifyErr != nil || !publicState.Identity.equal(*restore.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch backup-restored source changed"),
			verifyErr,
		)
	}
	return transaction.finishBackupRestoredSourceCleanup(operation, restore)
}

func requireApplyPatchTxnRootedAbsent(
	anchor *applyPatchTxnAnchor,
	location *applyPatchTransactionJournalRootedLocation,
) error {
	if location == nil {
		return errors.New("apply-patch rooted artifact is unavailable")
	}
	for _, name := range []string{location.Basename, location.RemovalBasename} {
		_, _, err := applyPatchTxnIdentityAt(anchor, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return errors.Join(
			errors.New("apply-patch transaction expected an absent rooted artifact"),
			err,
		)
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) finishBackupRestoredSourceCleanup(
	operation *applyPatchTxnIntent,
	restore *applyPatchTransactionJournalArtifact,
) error {
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil {
		return err
	}
	quarantineAbsenceErr := requireApplyPatchTxnRootedAbsent(
		operation.source.anchor,
		quarantine.Rooted,
	)
	if quarantineAbsenceErr != nil {
		return quarantineAbsenceErr
	}
	if restore == nil || restore.Rooted == nil || restore.Rooted.Identity == nil {
		return errors.New("apply-patch backup restore witness is unavailable")
	}
	if transaction.effects.sourceRestoreRequired != nil {
		transaction.effects.sourceRestoreRequired[operation.index] = false
	}
	return transaction.checkpoint()
}

func (transaction *applyPatchPreparedTransaction) rollbackPublishedTarget(
	operation *applyPatchTxnIntent,
) error {
	stage, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageStage,
	)
	if artifactErr != nil || stage.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch postimage identity is unavailable"),
			artifactErr,
		)
	}
	rollback, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactTargetRollbackQuarantine,
	)
	if artifactErr != nil {
		return artifactErr
	}
	quarantineErr := applyPatchTxnQuarantineExact(
		operation.targetAnchor,
		operation.targetLayout.components[0],
		rollback.Rooted.Basename,
		*stage.Rooted.Identity,
	)
	if quarantineErr != nil {
		return quarantineErr
	}
	rollback.Rooted.Identity = copyApplyPatchTxnIdentity(*stage.Rooted.Identity)
	rollback.Rooted.Links = 2
	transaction.effects.targetPublished[operation.index] = false
	transaction.effects.targetRollbackQuarantined[operation.index] = true
	syncErr := applyPatchTxnSyncDirectory(operation.targetAnchor)
	if syncErr != nil {
		return syncErr
	}
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		return checkpointErr
	}
	return transaction.finishRollbackQuarantinedTarget(operation)
}

func (transaction *applyPatchPreparedTransaction) finishRollbackQuarantinedTarget(
	operation *applyPatchTxnIntent,
) error {
	rollback, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactTargetRollbackQuarantine,
	)
	if artifactErr != nil || rollback.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch rollback quarantine is unavailable"),
			artifactErr,
		)
	}
	if operation.planned.kind == "update" &&
		(transaction.effects.sourceQuarantined[operation.index] ||
			transaction.effects.sourceRestoreRequired[operation.index]) {
		restoreErr := transaction.restoreSource(operation)
		if restoreErr != nil {
			return restoreErr
		}
	}
	removeErr := transaction.removeRooted(operation.targetAnchor, rollback.Rooted, false)
	if removeErr != nil {
		return removeErr
	}
	transaction.effects.targetRollbackQuarantined[operation.index] = false
	if faultErr := transaction.injectFault(fmt.Sprintf(
		"rollback_target_quarantine_removed:%d",
		operation.index,
	)); faultErr != nil {
		return faultErr
	}
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if artifactErr != nil || witness.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch postimage witness is unavailable"),
			artifactErr,
		)
	}
	if err := transaction.removeRooted(operation.targetAnchor, witness.Rooted, false); err != nil {
		return err
	}
	if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
		return err
	}
	return transaction.checkpoint()
}

func cleanupApplyPatchTxnUnpublishedTarget(
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
	checkpoint func() error,
) error {
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
	}
	return applyPatchTxnSyncDirectory(operation.targetAnchor)
}

func (transaction *applyPatchPreparedTransaction) rollbackPublishedForest(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
) error {
	if forest.StageRoot.Identity == nil || forest.SentinelWitness.Identity == nil {
		return errors.New("apply-patch forest identity is unavailable")
	}
	if err := applyPatchTxnQuarantineExact(
		intent.anchor,
		intent.publicRoot,
		intent.rollbackRoot,
		*forest.StageRoot.Identity,
	); err != nil {
		return err
	}
	forest.RollbackRoot.Identity = copyApplyPatchTxnIdentity(*forest.StageRoot.Identity)
	transaction.effects.forestPublished[intent.id] = false
	transaction.effects.forestRollbackQuarantined[intent.id] = true
	if err := applyPatchTxnSyncDirectory(intent.anchor); err != nil {
		return err
	}
	if err := transaction.checkpoint(); err != nil {
		return err
	}
	if err := removeApplyPatchTxnForestTree(
		intent,
		forest,
		intent.rollbackRoot,
		transaction.checkpoint,
	); err != nil {
		return err
	}
	transaction.effects.forestRollbackQuarantined[intent.id] = false
	return transaction.checkpoint()
}

func removeApplyPatchTxnForestTree(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	rootName string,
	checkpoint func() error,
) error {
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
	rootPath := filepath.Join(intent.anchorPath, rootName)
	for entryIndex := len(forest.Entries) - 1; entryIndex >= 1; entryIndex-- {
		entry := &forest.Entries[entryIndex]
		if entry.Identity == nil {
			return errors.New("apply-patch forest entry identity is unavailable")
		}
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		parentPath := rootPath
		if parentRelative != "." {
			parentPath = filepath.Join(rootPath, parentRelative)
		}
		parent, err := openApplyPatchTxnAnchor(parentPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
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
			return errors.Join(removeErr, closeErr)
		}
	}
	rootLocation := &forest.StageRoot
	if rootName == forest.RollbackRoot.Basename {
		rootLocation = &forest.RollbackRoot
	}
	if rootLocation.Identity == nil {
		return errors.New("apply-patch forest root identity is unavailable")
	}
	if err := removeApplyPatchTxnRootedWithCheckpoint(
		intent.anchor,
		rootLocation,
		true,
		checkpoint,
	); err != nil {
		return err
	}
	return applyPatchTxnSyncDirectory(intent.anchor)
}

func (transaction *applyPatchPreparedTransaction) cleanupCommitted() error {
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		return err
	}
	if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
		return err
	}
	return transaction.store.finishCommittedStateCleanup()
}

func (transaction *applyPatchPreparedTransaction) cleanupCommittedPublicArtifacts() error {
	for _, operation := range transaction.intent.operations {
		if operation.source != nil {
			for _, role := range []applyPatchTransactionArtifactRole{
				applyPatchTransactionArtifactSourceQuarantine,
				applyPatchTransactionArtifactSourceWitness,
			} {
				artifact, err := requireApplyPatchTxnArtifact(
					transaction.journal,
					operation.index,
					role,
				)
				if err != nil || artifact.Rooted.Identity == nil {
					return errors.Join(errors.New("apply-patch committed source artifact is unavailable"), err)
				}
				if err := transaction.removeRooted(
					operation.source.anchor,
					artifact.Rooted,
					false,
				); err != nil {
					return err
				}
			}
			if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
				return err
			}
		}
		if operation.targetAnchor != nil && operation.forest == nil {
			witness, err := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactPostimageWitness,
			)
			if err != nil || witness.Rooted.Identity == nil {
				return errors.Join(errors.New("apply-patch committed witness is unavailable"), err)
			}
			if err := transaction.removeRooted(
				operation.targetAnchor,
				witness.Rooted,
				false,
			); err != nil {
				return err
			}
			if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
				return err
			}
		}
	}
	for _, forest := range transaction.intent.forests {
		journalForest, err := requireApplyPatchTxnJournalForest(
			transaction.journal,
			forest.id,
		)
		if err != nil || journalForest.SentinelWitness.Identity == nil {
			return errors.Join(errors.New("apply-patch committed forest witness is unavailable"), err)
		}
		if err := transaction.removeRooted(
			forest.anchor,
			&journalForest.SentinelWitness,
			false,
		); err != nil {
			return err
		}
		if err := applyPatchTxnSyncDirectory(forest.anchor); err != nil {
			return err
		}
	}
	return nil
}

func removeApplyPatchTxnIfPresent(
	anchor *applyPatchTxnAnchor,
	basename string,
	removalBasename string,
	expected applyPatchTxnIdentity,
	directory bool,
) error {
	return applyPatchTxnRemoveExact(
		anchor,
		basename,
		removalBasename,
		expected,
		directory,
	)
}

func removeApplyPatchTxnRootedWithCheckpoint(
	anchor *applyPatchTxnAnchor,
	location *applyPatchTransactionJournalRootedLocation,
	directory bool,
	checkpoint func() error,
) error {
	if location == nil || location.Identity == nil || checkpoint == nil {
		return errors.New("apply-patch rooted removal state is unavailable")
	}
	if !location.RemovalAttempted {
		alreadyRemoved, err := preclassifyApplyPatchTxnRemoval(
			anchor,
			location.Basename,
			location.RemovalBasename,
			*location.Identity,
		)
		if err != nil || alreadyRemoved {
			return err
		}
		location.RemovalAttempted = true
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := removeApplyPatchTxnIfPresent(
		anchor,
		location.Basename,
		location.RemovalBasename,
		*location.Identity,
		directory,
	); err != nil {
		return err
	}
	if err := applyPatchTxnSyncDirectory(anchor); err != nil {
		return err
	}
	location.RemovalAttempted = false
	return checkpoint()
}

func removeApplyPatchTxnForestEntryWithCheckpoint(
	anchor *applyPatchTxnAnchor,
	basename string,
	entry *applyPatchTransactionJournalForestEntry,
	checkpoint func() error,
) error {
	if entry == nil || entry.Identity == nil || checkpoint == nil {
		return errors.New("apply-patch forest removal state is unavailable")
	}
	if !entry.RemovalAttempted {
		alreadyRemoved, err := preclassifyApplyPatchTxnRemoval(
			anchor,
			basename,
			entry.RemovalBasename,
			*entry.Identity,
		)
		if err != nil || alreadyRemoved {
			return err
		}
		entry.RemovalAttempted = true
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := removeApplyPatchTxnIfPresent(
		anchor,
		basename,
		entry.RemovalBasename,
		*entry.Identity,
		entry.Kind == "directory",
	); err != nil {
		return err
	}
	if err := applyPatchTxnSyncDirectory(anchor); err != nil {
		return err
	}
	entry.RemovalAttempted = false
	return checkpoint()
}

func preclassifyApplyPatchTxnRemoval(
	anchor *applyPatchTxnAnchor,
	basename string,
	removalBasename string,
	expected applyPatchTxnIdentity,
) (bool, error) {
	source, _, sourceErr := applyPatchTxnIdentityAt(anchor, basename)
	removal, _, removalErr := applyPatchTxnIdentityAt(anchor, removalBasename)
	sourceAbsent := errors.Is(sourceErr, os.ErrNotExist)
	removalAbsent := errors.Is(removalErr, os.ErrNotExist)
	switch {
	case sourceAbsent && removalAbsent:
		return true, nil
	case sourceErr == nil && source.equal(expected) && removalAbsent:
		return false, nil
	case sourceErr == nil && !source.equal(expected):
		return false, errors.New("apply-patch removal source identity conflict")
	case removalErr == nil:
		_ = removal
		return false, errors.New("apply-patch unexpected removal quarantine")
	default:
		return false, errors.Join(
			errors.New("apply-patch removal preclassification failed"),
			sourceErr,
			removalErr,
		)
	}
}

func (transaction *applyPatchPreparedTransaction) removeRooted(
	anchor *applyPatchTxnAnchor,
	location *applyPatchTransactionJournalRootedLocation,
	directory bool,
) error {
	return removeApplyPatchTxnRootedWithCheckpoint(
		anchor,
		location,
		directory,
		transaction.checkpoint,
	)
}

func verifyApplyPatchTxnCommittedPublicState(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	effects applyPatchTxnEffects,
	allowMissingWitnessAfterAuthenticatedCleanup ...bool,
) error {
	allowMissingWitness := len(allowMissingWitnessAfterAuthenticatedCleanup) > 0 &&
		allowMissingWitnessAfterAuthenticatedCleanup[0]
	for _, operation := range intent.operations {
		journalOperation := &journal.Operations[operation.index]
		if operation.source != nil && effects.sourceQuarantined[operation.index] {
			if err := verifyApplyPatchTxnRetainedOriginal(operation, journal); err != nil {
				return err
			}
		}
		if operation.planned.kind == "delete" || operation.planned.kind == "move" {
			if err := requireApplyPatchTxnAbsent(
				operation.source.anchor,
				operation.source.basename,
			); err != nil {
				return err
			}
		}
		if operation.forest != nil || operation.targetAnchor == nil {
			continue
		}
		stage, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil || stage.Rooted.Identity == nil ||
			!effects.targetPublished[operation.index] {
			return errors.Join(errors.New("apply-patch target was not published"), err)
		}
		expectedLinks, err := committedApplyPatchTxnPostimageLinks(
			operation,
			journal,
			allowMissingWitness,
		)
		if err != nil {
			return err
		}
		state, err := verifyApplyPatchTxnRegular(
			operation.targetAnchor,
			operation.targetLayout.components[0],
			journalOperation.After,
			expectedLinks,
		)
		if err != nil || !state.Identity.equal(*stage.Rooted.Identity) {
			return errors.Join(errors.New("apply-patch published target changed"), err)
		}
		if err := requireApplyPatchTxnAbsent(
			operation.targetAnchor,
			operation.stageName,
		); err != nil {
			return err
		}
	}
	for _, forestIntent := range intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil || !effects.forestPublished[forestIntent.id] {
			return errors.Join(errors.New("apply-patch forest was not published"), err)
		}
		if err := verifyApplyPatchTxnPublishedForest(
			forestIntent,
			forest,
			allowMissingWitness,
		); err != nil {
			return err
		}
	}
	return nil
}

func verifyApplyPatchTxnRetainedOriginal(
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
) error {
	if operation == nil || operation.source == nil || journal == nil {
		return errors.New("apply-patch retained original is unavailable")
	}
	quarantine, err := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	witness, witnessErr := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if err != nil || witnessErr != nil || quarantine.Rooted.Identity == nil ||
		witness.Rooted.Identity == nil {
		return errors.Join(
			errors.New("apply-patch retained original metadata is unavailable"),
			err,
			witnessErr,
		)
	}
	before := journal.Operations[operation.index].Before
	quarantineState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		before,
		quarantine.Rooted.Links,
	)
	if verifyErr != nil || !quarantineState.Identity.equal(*quarantine.Rooted.Identity) ||
		!quarantineState.Identity.equal(*witness.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch retained original quarantine changed"),
			verifyErr,
		)
	}
	witnessState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		witness.Rooted.Basename,
		before,
		witness.Rooted.Links,
	)
	if verifyErr != nil || !witnessState.Identity.equal(quarantineState.Identity) {
		return errors.Join(
			errors.New("apply-patch retained original witness changed"),
			verifyErr,
		)
	}
	return nil
}

func committedApplyPatchTxnPostimageLinks(
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
	allowMissingWitness bool,
) (uint64, error) {
	witness, err := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if err != nil || witness.Rooted.Identity == nil {
		return 0, errors.Join(
			errors.New("apply-patch committed witness metadata is unavailable"),
			err,
		)
	}
	present, err := applyPatchTxnRecoveryIdentityPresent(
		operation.targetAnchor,
		witness.Rooted.Basename,
		*witness.Rooted.Identity,
	)
	if err != nil {
		return 0, err
	}
	if present {
		return 2, nil
	}
	if !allowMissingWitness {
		return 0, errors.New("apply-patch committed witness is missing")
	}
	return 1, nil
}

func verifyApplyPatchTxnPublishedForest(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	allowMissingWitness bool,
) error {
	if err := requireApplyPatchTxnAbsent(intent.anchor, intent.stageRoot); err != nil {
		return err
	}
	return verifyApplyPatchTxnForestTreeAt(
		intent,
		forest,
		intent.publicRoot,
		allowMissingWitness,
	)
}

func verifyApplyPatchTxnForestTreeAt(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	rootName string,
	allowMissingWitness bool,
) error {
	if forest.StageRoot.Identity == nil || forest.SentinelWitness.Identity == nil {
		return errors.New("apply-patch forest identity is unavailable")
	}
	rootState, err := applyPatchTxnInspectAt(intent.anchor, rootName)
	if err != nil || !rootState.Identity.equal(*forest.StageRoot.Identity) {
		return errors.Join(errors.New("apply-patch published forest root changed"), err)
	}
	publicRootPath := filepath.Join(intent.anchorPath, rootName)
	witnessPresent, err := applyPatchTxnRecoveryIdentityPresent(
		intent.anchor,
		forest.SentinelWitness.Basename,
		*forest.SentinelWitness.Identity,
	)
	if err != nil {
		return err
	}
	if !witnessPresent && !allowMissingWitness {
		return errors.New("apply-patch forest witness is missing")
	}
	for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
		entry := &forest.Entries[entryIndex]
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		parentPath := publicRootPath
		if parentRelative != "." {
			parentPath = filepath.Join(publicRootPath, parentRelative)
		}
		parent, openErr := openApplyPatchTxnAnchor(parentPath)
		if openErr != nil {
			return openErr
		}
		basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
		if entry.Kind == "directory" {
			state, inspectErr := applyPatchTxnInspectAt(parent, basename)
			_ = parent.Close()
			if inspectErr != nil || entry.Identity == nil ||
				!state.Identity.equal(*entry.Identity) || uint32(state.Mode.Perm()) != entry.Mode {
				return errors.Join(errors.New("apply-patch published forest directory changed"), inspectErr)
			}
			continue
		}
		expectedLinks := entry.Links
		if entry.RelativePath == forest.SentinelRelativePath && !witnessPresent {
			expectedLinks = 1
		}
		state, inspectErr := verifyApplyPatchTxnRegular(
			parent,
			basename,
			mapApplyPatchTxnForestEntryState(*entry),
			expectedLinks,
		)
		_ = parent.Close()
		if inspectErr != nil || entry.Identity == nil || !state.Identity.equal(*entry.Identity) {
			return errors.Join(errors.New("apply-patch published forest file changed"), inspectErr)
		}
	}
	return verifyApplyPatchTxnForestManifestAt(intent, forest, rootName)
}

func verifyApplyPatchTxnRollingForestTreeAt(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	rootName string,
) error {
	if intent == nil || forest == nil || forest.StageRoot.Identity == nil ||
		forest.SentinelWitness.Identity == nil {
		return errors.New("apply-patch rolling forest identity is unavailable")
	}
	rootState, err := applyPatchTxnInspectAt(intent.anchor, rootName)
	if err != nil || !rootState.Identity.equal(*forest.StageRoot.Identity) {
		return errors.Join(errors.New("apply-patch rolling forest root changed"), err)
	}
	witnessPresent, err := applyPatchTxnRecoveryIdentityPresent(
		intent.anchor,
		forest.SentinelWitness.Basename,
		*forest.SentinelWitness.Identity,
	)
	if err != nil {
		return err
	}
	if witnessPresent {
		sentinel := findApplyPatchTxnForestSentinel(forest)
		if sentinel == nil {
			return errors.New("apply-patch rolling forest sentinel is unavailable")
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			intent.anchor,
			forest.SentinelWitness.Basename,
			mapApplyPatchTxnForestEntryState(*sentinel),
			2,
		)
		if verifyErr != nil || !state.Identity.equal(*forest.SentinelWitness.Identity) {
			return errors.Join(
				errors.New("apply-patch rolling forest witness changed"),
				verifyErr,
			)
		}
	}
	rootPath := filepath.Join(intent.anchorPath, rootName)
	for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
		entry := &forest.Entries[entryIndex]
		if entry.Identity == nil {
			return errors.New("apply-patch rolling forest entry is uncheckpointed")
		}
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		parentPath := rootPath
		if parentRelative != "." {
			parentPath = filepath.Join(rootPath, parentRelative)
		}
		parent, openErr := openApplyPatchTxnAnchor(parentPath)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
		identity, _, inspectErr := applyPatchTxnIdentityAt(parent, basename)
		if errors.Is(inspectErr, os.ErrNotExist) {
			_ = parent.Close()
			continue
		}
		if inspectErr != nil || !identity.equal(*entry.Identity) {
			_ = parent.Close()
			return errors.Join(
				errors.New("apply-patch rolling forest entry identity conflict"),
				inspectErr,
			)
		}
		if entry.Kind == "directory" {
			state, stateErr := applyPatchTxnInspectAt(parent, basename)
			_ = parent.Close()
			if stateErr != nil || uint32(state.Mode.Perm()) != entry.Mode {
				return errors.Join(
					errors.New("apply-patch rolling forest directory changed"),
					stateErr,
				)
			}
			continue
		}
		expectedLinks := entry.Links
		if entry.RelativePath == forest.SentinelRelativePath && !witnessPresent {
			expectedLinks = 1
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			parent,
			basename,
			mapApplyPatchTxnForestEntryState(*entry),
			expectedLinks,
		)
		_ = parent.Close()
		if verifyErr != nil || !state.Identity.equal(*entry.Identity) {
			return errors.Join(
				errors.New("apply-patch rolling forest file changed"),
				verifyErr,
			)
		}
	}
	expected := expectedApplyPatchTxnForestChildren(forest)
	for relative, wantedAll := range expected {
		path := rootPath
		if relative != "." {
			path = filepath.Join(rootPath, filepath.FromSlash(relative))
		}
		anchor, openErr := openApplyPatchTxnAnchor(path)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		actual, readErr := applyPatchTxnReadDirectoryNames(anchor, len(wantedAll))
		closeErr := anchor.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		allowed := make(map[string]struct{}, len(wantedAll))
		for _, wanted := range wantedAll {
			allowed[wanted] = struct{}{}
		}
		for _, name := range actual {
			if _, ok := allowed[name]; !ok {
				return errors.New("apply-patch rolling forest has an alien entry")
			}
		}
	}
	return nil
}
