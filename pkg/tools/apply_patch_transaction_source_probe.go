package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

func probeApplyPatchTxnSourceFallbackCapabilities(
	ctx context.Context,
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
	fault func(string) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if intent == nil || journal == nil || checkpoint == nil ||
		journal.Phase != applyPatchTransactionPhasePreparing {
		return errors.New("apply-patch source capability probe state is unavailable")
	}
	proven, err := applyPatchTxnStagedWitnessFilesystems(intent, journal)
	if err != nil {
		return err
	}
	for _, operation := range intent.operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if operation == nil || operation.source == nil {
			continue
		}
		device := operation.source.anchor.identity.Device
		if device == 0 {
			return errors.New("apply-patch source capability filesystem is unavailable")
		}
		if _, exists := proven[device]; exists {
			continue
		}
		proven[device] = struct{}{}
		if err := probeApplyPatchTxnOneSourceFallback(
			ctx,
			operation,
			journal,
			checkpoint,
			fault,
		); err != nil {
			return fmt.Errorf(
				"probe apply-patch source fallback capabilities: %w",
				err,
			)
		}
	}
	return ctx.Err()
}

func applyPatchTxnStagedWitnessFilesystems(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) (map[uint64]struct{}, error) {
	proven := make(map[uint64]struct{})
	for _, operation := range intent.operations {
		if operation == nil || operation.planned.kind == "add" ||
			operation.targetAnchor == nil || operation.forest != nil {
			continue
		}
		witness, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil || witness.Rooted.Identity == nil {
			return nil, errors.Join(
				errors.New("apply-patch staged capability witness is unavailable"),
				err,
			)
		}
		proven[operation.targetAnchor.identity.Device] = struct{}{}
	}
	for _, forest := range intent.forests {
		if forest == nil {
			continue
		}
		preservedMode := false
		for _, operation := range forest.operations {
			if operation != nil && operation.planned.kind != "add" {
				preservedMode = true
				break
			}
		}
		if !preservedMode {
			continue
		}
		checkpointed, err := requireApplyPatchTxnJournalForest(journal, forest.id)
		if err != nil || checkpointed.SentinelWitness.Identity == nil {
			return nil, errors.Join(
				errors.New("apply-patch staged forest capability witness is unavailable"),
				err,
			)
		}
		proven[forest.anchor.identity.Device] = struct{}{}
	}
	return proven, nil
}

func probeApplyPatchTxnOneSourceFallback(
	ctx context.Context,
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
	fault func(string) error,
) error {
	if operation == nil || operation.source == nil {
		return errors.New("apply-patch source capability probe endpoint is unavailable")
	}
	stage, artifactErr := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceRestoreStage,
	)
	if artifactErr != nil {
		return artifactErr
	}
	witness, artifactErr := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceProbeWitness,
	)
	if artifactErr != nil {
		return artifactErr
	}
	if stage.Rooted.Identity != nil || witness.Rooted.Identity != nil {
		return errors.New("apply-patch source capability probe is already active")
	}
	if err := applyPatchTxnProbeNoReplace(
		operation.source.anchor,
		operation.source.basename,
		operation.source.state.Identity,
	); err != nil {
		return err
	}

	file, identity, createErr := applyPatchTxnCreateRegular(
		operation.source.anchor,
		stage.Rooted.Basename,
		0o600,
	)
	if createErr != nil {
		return createErr
	}
	closeRequired := true
	defer func() {
		if closeRequired {
			_ = file.Close()
		}
	}()
	stage.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	stage.Rooted.Links = 1
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		return err
	}
	if err := checkpoint(journal); err != nil {
		return err
	}
	if err := runApplyPatchTxnSourceProbeFault(fault, "created", operation.index); err != nil {
		return err
	}
	if err := applyPatchTxnWriteRegularContext(
		ctx,
		file,
		operation.planned.before,
		operation.planned.mode,
		true,
	); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close apply-patch source capability stage: %w", err)
	}
	closeRequired = false
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		return err
	}
	stageState, verifyErr := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		stage.Rooted.Basename,
		stage.Expected,
		1,
	)
	if verifyErr != nil || !stageState.Identity.equal(identity) ||
		stageState.Mode.Perm() != operation.planned.mode.Perm() {
		return errors.Join(
			errors.New("apply-patch source capability stage changed"),
			verifyErr,
		)
	}
	if err := checkpoint(journal); err != nil {
		return err
	}
	if err := runApplyPatchTxnSourceProbeFault(fault, "written", operation.index); err != nil {
		return err
	}
	if err := applyPatchTxnLinkWitness(
		operation.source.anchor,
		stage.Rooted.Basename,
		identity,
		2,
		operation.source.anchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
	); err != nil {
		return err
	}
	stage.Rooted.Links = 2
	witness.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	witness.Rooted.Links = 2
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		return err
	}
	for _, location := range []*applyPatchTransactionJournalRootedLocation{
		stage.Rooted,
		witness.Rooted,
	} {
		state, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			location.Basename,
			stage.Expected,
			2,
		)
		if verifyErr != nil || !state.Identity.equal(identity) {
			return errors.Join(
				errors.New("apply-patch source capability witness changed"),
				verifyErr,
			)
		}
	}
	if err := checkpoint(journal); err != nil {
		return err
	}
	if err := runApplyPatchTxnSourceProbeFault(fault, "linked", operation.index); err != nil {
		return err
	}
	checkpointCurrent := func() error { return checkpoint(journal) }
	if err := removeApplyPatchTxnRootedWithCheckpoint(
		operation.source.anchor,
		witness.Rooted,
		false,
		checkpointCurrent,
	); err != nil {
		return err
	}
	resetApplyPatchTxnSourceProbeArtifact(witness.Rooted)
	stage.Rooted.Links = 1
	if err := checkpoint(journal); err != nil {
		return err
	}
	if err := removeApplyPatchTxnRootedWithCheckpoint(
		operation.source.anchor,
		stage.Rooted,
		false,
		checkpointCurrent,
	); err != nil {
		return err
	}
	resetApplyPatchTxnSourceProbeArtifact(stage.Rooted)
	if err := checkpoint(journal); err != nil {
		return err
	}
	return runApplyPatchTxnSourceProbeFault(fault, "cleaned", operation.index)
}

func runApplyPatchTxnSourceProbeFault(
	fault func(string) error,
	boundary string,
	operationIndex int,
) error {
	if fault == nil {
		return nil
	}
	return fault(fmt.Sprintf("source_fallback_probe_%s:%d", boundary, operationIndex))
}

func resetApplyPatchTxnSourceProbeArtifact(
	location *applyPatchTransactionJournalRootedLocation,
) {
	if location == nil {
		return
	}
	location.Identity = nil
	location.Links = 0
	location.RemovalAttempted = false
}

func validateApplyPatchTxnPreEffectDeclaredNames(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) error {
	if intent == nil || journal == nil ||
		journal.Phase != applyPatchTransactionPhasePreparing &&
			journal.Phase != applyPatchTransactionPhasePrepared {
		return errors.New("apply-patch pre-effect declared-name state is unavailable")
	}
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Rooted == nil {
			continue
		}
		if err := validateApplyPatchTxnPreEffectRootedNames(artifact.Rooted); err != nil {
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
			if err := validateApplyPatchTxnPreEffectRootedNames(location); err != nil {
				return err
			}
		}
		stageRootPath := filepath.Join(forestIntent.anchorPath, forestIntent.stageRoot)
		for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
			entry := &forest.Entries[entryIndex]
			if entry.RemovalAttempted {
				return errors.New("apply-patch pre-effect forest removal state is invalid")
			}
			parentPath := stageRootPath
			parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
			if parentRelative != "." {
				parentPath = filepath.Join(stageRootPath, parentRelative)
			}
			parent, openErr := openApplyPatchTxnAnchor(parentPath)
			if openErr != nil {
				return openErr
			}
			absenceErr := requireApplyPatchTxnAbsent(parent, entry.RemovalBasename)
			closeErr := parent.Close()
			if absenceErr != nil || closeErr != nil {
				return errors.Join(
					errors.New("apply-patch pre-effect forest removal name is present"),
					absenceErr,
					closeErr,
				)
			}
		}
	}
	return nil
}

func validateApplyPatchTxnPreEffectRootedNames(
	location *applyPatchTransactionJournalRootedLocation,
) error {
	if location == nil || location.RemovalAttempted {
		return errors.New("apply-patch pre-effect rooted state is invalid")
	}
	anchor, err := openApplyPatchTxnAnchor(location.AnchorCanonicalPath)
	if err != nil || !anchor.identity.equal(location.AnchorIdentity) {
		if anchor != nil {
			_ = anchor.Close()
		}
		return errors.Join(
			errors.New("apply-patch pre-effect artifact anchor changed"),
			err,
		)
	}
	if err := requireApplyPatchTxnAbsent(anchor, location.RemovalBasename); err != nil {
		return errors.Join(
			errors.New("apply-patch pre-effect removal name is present"),
			anchor.Close(),
		)
	}
	if location.Identity != nil {
		return anchor.Close()
	}
	if err := requireApplyPatchTxnAbsent(anchor, location.Basename); err != nil {
		return errors.Join(
			errors.New("apply-patch pre-effect artifact name is present"),
			anchor.Close(),
		)
	}
	return anchor.Close()
}

func validateApplyPatchTxnInactiveSourceProbeNames(
	operation *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
) error {
	if operation == nil || operation.source == nil || journal == nil {
		return errors.New("apply-patch source probe validation state is unavailable")
	}
	for _, role := range []applyPatchTransactionArtifactRole{
		applyPatchTransactionArtifactSourceRestoreStage,
		applyPatchTransactionArtifactSourceProbeWitness,
	} {
		artifact, err := requireApplyPatchTxnArtifact(journal, operation.index, role)
		if err != nil {
			return err
		}
		if artifact.Rooted.Identity != nil {
			continue
		}
		if err := requireApplyPatchTxnRootedAbsent(
			operation.source.anchor,
			artifact.Rooted,
		); err != nil {
			return errors.New("apply-patch inactive source probe artifact is present")
		}
	}
	return nil
}
