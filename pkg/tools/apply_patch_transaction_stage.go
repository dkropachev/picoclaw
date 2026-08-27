package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type applyPatchTxnJournalCheckpoint func(*applyPatchTransactionJournal) error

func validateApplyPatchTxnIntentNamesAbsent(intent *applyPatchTxnIntentPlan) error {
	if intent == nil {
		return errors.New("apply-patch transaction intent is unavailable")
	}
	type declaredName struct {
		anchor *applyPatchTxnAnchor
		name   string
	}
	declared := make([]declaredName, 0, len(intent.operations)*7+len(intent.forests)*3)
	for _, operation := range intent.operations {
		if operation.source != nil {
			for _, name := range []string{
				operation.sourceWitnessName,
				operation.sourceProbeWitness,
				operation.sourceQuarantine,
				operation.sourceRestoreStage,
			} {
				declared = append(declared, declaredName{operation.source.anchor, name})
			}
		}
		if operation.targetAnchor != nil {
			for _, name := range []string{
				operation.stageName,
				operation.postWitnessName,
				operation.targetRollback,
			} {
				declared = append(declared, declaredName{operation.targetAnchor, name})
			}
		}
	}
	for _, forest := range intent.forests {
		for _, name := range []string{
			forest.stageRoot,
			forest.rollbackRoot,
			forest.sentinelWitnessName,
		} {
			declared = append(declared, declaredName{forest.anchor, name})
		}
	}
	seen := make(map[string]struct{}, len(declared))
	for _, candidate := range declared {
		if candidate.anchor == nil {
			return errors.New("apply-patch transaction declared anchor is unavailable")
		}
		if err := validateApplyPatchTxnBasename(candidate.name); err != nil {
			return err
		}
		key := candidate.anchor.canonical + "\x00" + candidate.name
		if _, duplicate := seen[key]; duplicate {
			return errors.New("apply-patch transaction private name is duplicated")
		}
		seen[key] = struct{}{}
		_, _, err := applyPatchTxnIdentityAt(candidate.anchor, candidate.name)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.Join(
				errors.New("apply-patch transaction private name is not absent"),
				err,
			)
		}
	}
	return nil
}

func stageApplyPatchTxnPostimages(
	ctx context.Context,
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if intent == nil || journal == nil ||
		journal.Phase != applyPatchTransactionPhasePreparing ||
		len(intent.operations) != len(journal.Operations) {
		return errors.New("apply-patch transaction staging state is invalid")
	}
	if checkpoint == nil {
		return errors.New("apply-patch transaction journal checkpoint is unavailable")
	}
	for _, operation := range intent.operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if operation.planned.targetPath == "" || operation.forest != nil {
			continue
		}
		if err := stageApplyPatchTxnRegularPostimage(
			ctx,
			operation,
			journal,
			checkpoint,
		); err != nil {
			return err
		}
	}
	for _, forest := range intent.forests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stageApplyPatchTxnForest(ctx, forest, journal, checkpoint); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func stageApplyPatchTxnRegularPostimage(
	ctx context.Context,
	intent *applyPatchTxnIntent,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	mode := applyPatchFileMode()
	preserveMode := intent.planned.kind != "add"
	if preserveMode {
		mode = 0o600
	}
	file, identity, createErr := applyPatchTxnCreateRegular(
		intent.targetAnchor,
		intent.stageName,
		mode,
	)
	if createErr != nil {
		return fmt.Errorf("stage patch target %q: %w", intent.planned.targetLabel, createErr)
	}
	closeRequired := true
	defer func() {
		if closeRequired {
			_ = file.Close()
		}
	}()
	stageArtifact, artifactErr := requireApplyPatchTxnArtifact(
		journal,
		intent.index,
		applyPatchTransactionArtifactPostimageStage,
	)
	if artifactErr != nil {
		return artifactErr
	}
	stageArtifact.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	stageArtifact.Rooted.Links = 1
	initialSyncErr := applyPatchTxnSyncDirectory(intent.targetAnchor)
	if initialSyncErr != nil {
		return initialSyncErr
	}
	if !preserveMode {
		state, inspectErr := applyPatchTxnInspectAt(intent.targetAnchor, intent.stageName)
		if inspectErr != nil {
			return inspectErr
		}
		setApplyPatchTxnObservedPostMode(journal, intent.index, state.Mode)
	}
	checkpointErr := checkpoint(journal)
	if checkpointErr != nil {
		return checkpointErr
	}
	writeErr := applyPatchTxnWriteRegularContext(
		ctx,
		file,
		intent.planned.after,
		intent.planned.mode,
		preserveMode,
	)
	if writeErr != nil {
		return writeErr
	}
	closeErr := file.Close()
	if closeErr != nil {
		return fmt.Errorf("close apply-patch transaction stage: %w", closeErr)
	}
	closeRequired = false
	postWriteSyncErr := applyPatchTxnSyncDirectory(intent.targetAnchor)
	if postWriteSyncErr != nil {
		return postWriteSyncErr
	}
	stageState, verifyErr := verifyApplyPatchTxnRegular(
		intent.targetAnchor,
		intent.stageName,
		journal.Operations[intent.index].After,
		1,
	)
	if verifyErr != nil || !stageState.Identity.equal(identity) {
		return errors.Join(
			errors.New("apply-patch transaction stage changed after creation"),
			verifyErr,
		)
	}
	checkpointErr = checkpoint(journal)
	if checkpointErr != nil {
		return checkpointErr
	}
	witnessArtifact, witnessArtifactErr := requireApplyPatchTxnArtifact(
		journal,
		intent.index,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if witnessArtifactErr != nil {
		return witnessArtifactErr
	}
	linkErr := applyPatchTxnLinkWitness(
		intent.targetAnchor,
		intent.stageName,
		identity,
		2,
		intent.targetAnchor,
		intent.postWitnessName,
		witnessArtifact.Rooted.RemovalBasename,
	)
	if linkErr != nil {
		return linkErr
	}
	witnessArtifact.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	stageArtifact.Rooted.Links = 2
	witnessArtifact.Rooted.Links = 2
	postLinkSyncErr := applyPatchTxnSyncDirectory(intent.targetAnchor)
	if postLinkSyncErr != nil {
		return postLinkSyncErr
	}
	witnessState, witnessErr := verifyApplyPatchTxnRegular(
		intent.targetAnchor,
		intent.postWitnessName,
		journal.Operations[intent.index].After,
		2,
	)
	if witnessErr != nil || !witnessState.Identity.equal(identity) {
		return errors.Join(
			errors.New("apply-patch transaction witness changed after creation"),
			witnessErr,
		)
	}
	stageState, stageInspectErr := applyPatchTxnInspectAt(intent.targetAnchor, intent.stageName)
	if stageInspectErr != nil || stageState.Links != 2 || !stageState.Identity.equal(identity) {
		return errors.Join(
			errors.New("apply-patch transaction stage link state changed"),
			stageInspectErr,
		)
	}
	return checkpoint(journal)
}

func stageApplyPatchTxnForest(
	ctx context.Context,
	intent *applyPatchTxnForestIntent,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	journalForest, forestErr := requireApplyPatchTxnJournalForest(journal, intent.id)
	if forestErr != nil {
		return forestErr
	}
	rootIdentity, mkdirErr := applyPatchTxnMkdir(
		intent.anchor,
		intent.stageRoot,
		applyPatchParentMode(),
	)
	if mkdirErr != nil {
		return mkdirErr
	}
	journalForest.StageRoot.Identity = copyApplyPatchTxnIdentity(rootIdentity)
	journalForest.Entries[0].Identity = copyApplyPatchTxnIdentity(rootIdentity)
	rootSyncErr := applyPatchTxnSyncDirectory(intent.anchor)
	if rootSyncErr != nil {
		return rootSyncErr
	}
	stageRoot, openErr := applyPatchTxnOpenChildDirectory(intent.anchor, intent.stageRoot)
	if openErr != nil {
		return openErr
	}
	defer stageRoot.Close()
	rootState, inspectErr := applyPatchTxnInspectAt(intent.anchor, intent.stageRoot)
	if inspectErr != nil || !rootState.Identity.equal(rootIdentity) {
		return errors.Join(
			errors.New("apply-patch transaction forest root changed after creation"),
			inspectErr,
		)
	}
	journalForest.Entries[0].Mode = uint32(rootState.Mode.Perm())
	if err := checkpoint(journal); err != nil {
		return err
	}
	stageRootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
	for entryIndex := 1; entryIndex < len(journalForest.Entries); entryIndex++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := &journalForest.Entries[entryIndex]
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		parentPath := stageRootPath
		if parentRelative != "." {
			parentPath = filepath.Join(stageRootPath, parentRelative)
		}
		parent, openErr := openApplyPatchTxnAnchor(parentPath)
		if openErr != nil {
			return openErr
		}
		basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
		if entry.Kind == "directory" {
			identity, mkdirErr := applyPatchTxnMkdir(parent, basename, applyPatchParentMode())
			if mkdirErr != nil {
				_ = parent.Close()
				return mkdirErr
			}
			entry.Identity = copyApplyPatchTxnIdentity(identity)
			if syncErr := applyPatchTxnSyncDirectory(parent); syncErr != nil {
				_ = parent.Close()
				return syncErr
			}
			state, inspectErr := applyPatchTxnInspectAt(parent, basename)
			_ = parent.Close()
			if inspectErr != nil || !state.Identity.equal(identity) {
				return errors.Join(
					errors.New("apply-patch transaction forest directory changed"),
					inspectErr,
				)
			}
			entry.Mode = uint32(state.Mode.Perm())
			if err := checkpoint(journal); err != nil {
				return err
			}
			continue
		}
		if entry.OperationIndex == nil {
			_ = parent.Close()
			return errors.New("apply-patch transaction forest file is unbound")
		}
		operationIndex := *entry.OperationIndex
		operationIntent := intentOperationByIndex(intent, operationIndex)
		if operationIntent == nil {
			_ = parent.Close()
			return errors.New("apply-patch transaction forest operation is unavailable")
		}
		createMode := applyPatchFileMode()
		if operationIntent.planned.kind != "add" {
			createMode = 0o600
		}
		file, identity, createErr := applyPatchTxnCreateRegular(parent, basename, createMode)
		if createErr != nil {
			_ = parent.Close()
			return createErr
		}
		entry.Identity = copyApplyPatchTxnIdentity(identity)
		entry.Links = 1
		if syncErr := applyPatchTxnSyncDirectory(parent); syncErr != nil {
			_ = file.Close()
			_ = parent.Close()
			return syncErr
		}
		createdState, inspectErr := applyPatchTxnInspectAt(parent, basename)
		if inspectErr != nil {
			_ = file.Close()
			_ = parent.Close()
			return inspectErr
		}
		if operationIntent.planned.kind == "add" {
			setApplyPatchTxnObservedPostMode(journal, operationIndex, createdState.Mode)
		}
		if err := checkpoint(journal); err != nil {
			_ = file.Close()
			_ = parent.Close()
			return err
		}
		preserveMode := operationIntent.planned.kind != "add"
		writeErr := applyPatchTxnWriteRegularContext(
			ctx,
			file,
			operationIntent.planned.after,
			operationIntent.planned.mode,
			preserveMode,
		)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = parent.Close()
			return errors.Join(writeErr, closeErr)
		}
		if syncErr := applyPatchTxnSyncDirectory(parent); syncErr != nil {
			_ = parent.Close()
			return syncErr
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			parent,
			basename,
			journal.Operations[operationIndex].After,
			1,
		)
		_ = parent.Close()
		if verifyErr != nil || !state.Identity.equal(identity) {
			return errors.Join(
				errors.New("apply-patch transaction forest file changed"),
				verifyErr,
			)
		}
		if err := checkpoint(journal); err != nil {
			return err
		}
	}
	return stageApplyPatchTxnForestSentinel(intent, journal, journalForest, checkpoint)
}

func stageApplyPatchTxnForestSentinel(
	intent *applyPatchTxnForestIntent,
	journal *applyPatchTransactionJournal,
	forest *applyPatchTransactionJournalForest,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	var sentinel *applyPatchTransactionJournalForestEntry
	for index := range forest.Entries {
		if forest.Entries[index].RelativePath == forest.SentinelRelativePath {
			sentinel = &forest.Entries[index]
			break
		}
	}
	if sentinel == nil || sentinel.Identity == nil {
		return errors.New("apply-patch transaction forest sentinel is unavailable")
	}
	stageRootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
	parentRelative := filepath.Dir(filepath.FromSlash(sentinel.RelativePath))
	parentPath := stageRootPath
	if parentRelative != "." {
		parentPath = filepath.Join(stageRootPath, parentRelative)
	}
	parent, openErr := openApplyPatchTxnAnchor(parentPath)
	if openErr != nil {
		return openErr
	}
	defer parent.Close()
	basename := filepath.Base(filepath.FromSlash(sentinel.RelativePath))
	linkErr := applyPatchTxnLinkWitness(
		parent,
		basename,
		*sentinel.Identity,
		2,
		intent.anchor,
		intent.sentinelWitnessName,
		forest.SentinelWitness.RemovalBasename,
	)
	if linkErr != nil {
		return linkErr
	}
	forest.SentinelWitness.Identity = copyApplyPatchTxnIdentity(*sentinel.Identity)
	forest.SentinelWitness.Links = 2
	sentinel.Links = 2
	syncErr := applyPatchTxnSyncDirectory(intent.anchor)
	if syncErr != nil {
		return syncErr
	}
	witness, verifyErr := verifyApplyPatchTxnRegular(
		intent.anchor,
		intent.sentinelWitnessName,
		mapApplyPatchTxnForestEntryState(*sentinel),
		2,
	)
	if verifyErr != nil || !witness.Identity.equal(*sentinel.Identity) {
		return errors.Join(
			errors.New("apply-patch transaction forest witness changed"),
			verifyErr,
		)
	}
	return checkpoint(journal)
}

func verifyApplyPatchTxnRegular(
	anchor *applyPatchTxnAnchor,
	basename string,
	expected applyPatchTransactionJournalFileState,
	expectedLinks uint64,
) (applyPatchTxnObjectState, error) {
	state, err := applyPatchTxnInspectAt(anchor, basename)
	if err != nil {
		return applyPatchTxnObjectState{}, err
	}
	if state.Identity.Kind != "regular" || state.Links != expectedLinks ||
		state.Size < 0 || uint64(state.Size) != expected.Length ||
		uint32(state.Mode.Perm()) != expected.Mode {
		return applyPatchTxnObjectState{}, errors.New(
			"apply-patch transaction file metadata does not match",
		)
	}
	data, mode, identity, err := applyPatchTxnReadRegular(
		anchor,
		basename,
		int64(expected.Length),
	)
	if err != nil || !identity.equal(state.Identity) || mode.Perm() != state.Mode.Perm() {
		return applyPatchTxnObjectState{}, errors.Join(
			errors.New("apply-patch transaction file changed while reading"),
			err,
		)
	}
	digestState := mapApplyPatchTxnFileState(true, data, mode)
	if digestState != expected {
		return applyPatchTxnObjectState{}, errors.New(
			"apply-patch transaction file content does not match",
		)
	}
	return state, nil
}

func requireApplyPatchTxnArtifact(
	journal *applyPatchTransactionJournal,
	operationIndex int,
	role applyPatchTransactionArtifactRole,
) (*applyPatchTransactionJournalArtifact, error) {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.OperationIndex == operationIndex && artifact.Role == role {
			return artifact, nil
		}
	}
	return nil, errors.New("apply-patch transaction journal artifact is missing")
}

func requireApplyPatchTxnJournalForest(
	journal *applyPatchTransactionJournal,
	id string,
) (*applyPatchTransactionJournalForest, error) {
	for index := range journal.Forests {
		if journal.Forests[index].ID == id {
			return &journal.Forests[index], nil
		}
	}
	return nil, errors.New("apply-patch transaction journal forest is missing")
}

func setApplyPatchTxnObservedPostMode(
	journal *applyPatchTransactionJournal,
	operationIndex int,
	mode os.FileMode,
) {
	observed := uint32(mode.Perm())
	journal.Operations[operationIndex].After.Mode = observed
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.OperationIndex == operationIndex &&
			artifact.Expected.Exists &&
			(artifact.Role == applyPatchTransactionArtifactPostimageStage ||
				artifact.Role == applyPatchTransactionArtifactPostimageWitness ||
				artifact.Role == applyPatchTransactionArtifactTargetRollbackQuarantine) {
			artifact.Expected.Mode = observed
		}
	}
	for forestIndex := range journal.Forests {
		for entryIndex := range journal.Forests[forestIndex].Entries {
			entry := &journal.Forests[forestIndex].Entries[entryIndex]
			if entry.OperationIndex != nil && *entry.OperationIndex == operationIndex {
				entry.Mode = observed
			}
		}
	}
}

func copyApplyPatchTxnIdentity(identity applyPatchTxnIdentity) *applyPatchTxnIdentity {
	detached := identity
	return &detached
}

func intentOperationByIndex(
	forest *applyPatchTxnForestIntent,
	index int,
) *applyPatchTxnIntent {
	for _, operation := range forest.operations {
		if operation.index == index {
			return operation
		}
	}
	return nil
}

func mapApplyPatchTxnForestEntryState(
	entry applyPatchTransactionJournalForestEntry,
) applyPatchTransactionJournalFileState {
	return applyPatchTransactionJournalFileState{
		Exists: true,
		Length: entry.Length,
		SHA256: entry.SHA256,
		Mode:   entry.Mode,
	}
}
