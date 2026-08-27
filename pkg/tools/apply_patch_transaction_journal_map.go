package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func newApplyPatchTxnWorkspaceBinding(
	workspace applyPatchWorkspace,
) (applyPatchTransactionWorkspaceBinding, error) {
	if workspace.canonical == "" || workspace.info == nil {
		return applyPatchTransactionWorkspaceBinding{}, errors.New(
			"apply-patch transaction workspace binding is unavailable",
		)
	}
	identity, err := applyPatchTxnIdentityFromFileInfo(workspace.info, "directory")
	if err != nil {
		return applyPatchTransactionWorkspaceBinding{}, err
	}
	digest := sha256.Sum256([]byte(workspace.canonical))
	return applyPatchTransactionWorkspaceBinding{
		CanonicalPath: workspace.canonical,
		PathSHA256:    hex.EncodeToString(digest[:]),
		Identity:      identity,
	}, nil
}

func newApplyPatchTxnStateBinding(
	canonicalRoot string,
	rootIdentity applyPatchTxnIdentity,
	key []byte,
	workspaceDirectory string,
	intent *applyPatchTxnIntentPlan,
) (applyPatchTransactionStateBinding, error) {
	if intent == nil {
		return applyPatchTransactionStateBinding{}, errors.New(
			"apply-patch transaction intent is unavailable",
		)
	}
	keyDigest := sha256.Sum256(key)
	return applyPatchTransactionStateBinding{
		CanonicalRoot:      canonicalRoot,
		RootIdentity:       rootIdentity,
		KeyID:              hex.EncodeToString(keyDigest[:]),
		WorkspaceDirectory: filepath.ToSlash(workspaceDirectory),
		ActiveDirectory:    intent.activeName,
		CommittedDirectory: intent.committedName,
	}, nil
}

func newApplyPatchTxnPreparingJournal(
	key []byte,
	workspace applyPatchTransactionWorkspaceBinding,
	state applyPatchTransactionStateBinding,
	intent *applyPatchTxnIntentPlan,
) (*applyPatchTransactionJournal, error) {
	if intent == nil || intent.plan == nil || len(intent.operations) == 0 {
		return nil, errors.New("apply-patch transaction intent is unavailable")
	}
	journal := &applyPatchTransactionJournal{
		Version:        applyPatchTransactionJournalVersion,
		Workspace:      workspace,
		State:          state,
		TransactionID:  intent.id,
		Phase:          applyPatchTransactionPhasePreparing,
		OperationCount: len(intent.operations),
		Operations:     make([]applyPatchTransactionJournalOperation, 0, len(intent.operations)),
		Artifacts:      make([]applyPatchTransactionJournalArtifact, 0, len(intent.operations)*8),
		Forests:        make([]applyPatchTransactionJournalForest, 0, len(intent.forests)),
	}
	for _, operation := range intent.operations {
		if operation == nil {
			return nil, errors.New("apply-patch transaction operation intent is unavailable")
		}
		mapped, err := mapApplyPatchTxnJournalOperation(operation)
		if err != nil {
			return nil, err
		}
		journal.Operations = append(journal.Operations, mapped)
		artifacts, err := mapApplyPatchTxnJournalArtifacts(key, intent.id, operation, mapped)
		if err != nil {
			return nil, err
		}
		journal.Artifacts = append(journal.Artifacts, artifacts...)
	}
	for _, forest := range intent.forests {
		mapped, err := mapApplyPatchTxnJournalForest(forest, journal.Operations)
		if err != nil {
			return nil, err
		}
		journal.Forests = append(journal.Forests, mapped)
	}
	if _, err := encodeApplyPatchTransactionJournal(key, journal); err != nil {
		return nil, fmt.Errorf("build apply-patch transaction preparing journal: %w", err)
	}
	return journal, nil
}

func mapApplyPatchTxnJournalOperation(
	intent *applyPatchTxnIntent,
) (applyPatchTransactionJournalOperation, error) {
	planned := intent.planned
	operation := applyPatchTransactionJournalOperation{
		Index: intent.index,
		Kind:  planned.kind,
		Before: mapApplyPatchTxnFileState(
			planned.source != nil,
			planned.before,
			planned.mode,
		),
		After: mapApplyPatchTxnFileState(
			planned.targetPath != "",
			planned.after,
			initialApplyPatchTxnPostMode(planned),
		),
	}
	if planned.source != nil {
		if intent.source == nil {
			return applyPatchTransactionJournalOperation{}, errors.New(
				"apply-patch transaction source intent is unavailable",
			)
		}
		identity := intent.source.state.Identity
		operation.Source = &applyPatchTransactionJournalEndpoint{
			Label:             planned.sourceLabel,
			CanonicalPath:     planned.sourcePath,
			PreflightIdentity: &identity,
			PreflightLinks:    intent.source.state.Links,
		}
	}
	if planned.targetPath != "" {
		operation.Target = &applyPatchTransactionJournalEndpoint{
			Label:         planned.targetLabel,
			CanonicalPath: planned.targetPath,
		}
		if planned.kind == "update" {
			identity := intent.source.state.Identity
			operation.Target.PreflightIdentity = &identity
			operation.Target.PreflightLinks = intent.source.state.Links
		}
	}
	if intent.forest != nil {
		operation.ForestID = intent.forest.id
	}
	return operation, nil
}

func mapApplyPatchTxnFileState(
	exists bool,
	data []byte,
	mode os.FileMode,
) applyPatchTransactionJournalFileState {
	if !exists {
		return applyPatchTransactionJournalFileState{}
	}
	digest := sha256.Sum256(data)
	return applyPatchTransactionJournalFileState{
		Exists: true,
		Length: uint64(len(data)),
		SHA256: hex.EncodeToString(digest[:]),
		Mode:   uint32(mode.Perm()),
	}
}

func initialApplyPatchTxnPostMode(planned plannedApplyPatchOp) os.FileMode {
	if planned.kind == "add" {
		// The actual safely umask-narrowed mode is checkpointed after exclusive
		// staging and before the journal advances to prepared.
		return 0
	}
	return planned.mode.Perm()
}

func mapApplyPatchTxnJournalArtifacts(
	key []byte,
	transactionID string,
	intent *applyPatchTxnIntent,
	operation applyPatchTransactionJournalOperation,
) ([]applyPatchTransactionJournalArtifact, error) {
	artifacts := make([]applyPatchTransactionJournalArtifact, 0, 8)
	if operation.Source != nil {
		backup, err := newApplyPatchTransactionBackupRecord(
			key,
			transactionID,
			intent.backupName,
			intent.planned.source.data,
		)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, applyPatchTransactionJournalArtifact{
			OperationIndex: intent.index,
			Role:           applyPatchTransactionArtifactBackupBlob,
			StateName:      intent.backupName,
			Expected:       operation.Before,
			Backup:         &backup,
		})
		for _, declared := range []struct {
			role applyPatchTransactionArtifactRole
			name string
		}{
			{applyPatchTransactionArtifactSourceRestoreStage, intent.sourceRestoreStage},
			{applyPatchTransactionArtifactSourceProbeWitness, intent.sourceProbeWitness},
			{applyPatchTransactionArtifactSourceWitness, intent.sourceWitnessName},
			{applyPatchTransactionArtifactSourceQuarantine, intent.sourceQuarantine},
		} {
			location, err := newApplyPatchTxnJournalLocation(
				intent.source.anchor,
				declared.name,
			)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, applyPatchTransactionJournalArtifact{
				OperationIndex: intent.index,
				Role:           declared.role,
				Rooted:         location,
				Expected:       operation.Before,
			})
		}
	}
	if operation.Target != nil && intent.forest == nil {
		for _, declared := range []struct {
			role applyPatchTransactionArtifactRole
			name string
		}{
			{applyPatchTransactionArtifactPostimageStage, intent.stageName},
			{applyPatchTransactionArtifactPostimageWitness, intent.postWitnessName},
			{applyPatchTransactionArtifactTargetRollbackQuarantine, intent.targetRollback},
		} {
			location, err := newApplyPatchTxnJournalLocation(
				intent.targetAnchor,
				declared.name,
			)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, applyPatchTransactionJournalArtifact{
				OperationIndex: intent.index,
				Role:           declared.role,
				Rooted:         location,
				Expected:       operation.After,
			})
		}
	}
	return artifacts, nil
}

func newApplyPatchTxnJournalLocation(
	anchor *applyPatchTxnAnchor,
	basename string,
) (*applyPatchTransactionJournalRootedLocation, error) {
	return &applyPatchTransactionJournalRootedLocation{
		AnchorCanonicalPath: anchor.canonical,
		AnchorIdentity:      anchor.identity,
		Basename:            basename,
	}, nil
}

func mapApplyPatchTxnJournalForest(
	intent *applyPatchTxnForestIntent,
	operations []applyPatchTransactionJournalOperation,
) (applyPatchTransactionJournalForest, error) {
	if intent == nil || intent.anchor == nil || len(intent.operations) == 0 {
		return applyPatchTransactionJournalForest{}, errors.New(
			"apply-patch transaction forest intent is unavailable",
		)
	}
	publicRoot := filepath.Join(intent.anchorPath, intent.publicRoot)
	stageRoot, err := newApplyPatchTxnJournalLocation(intent.anchor, intent.stageRoot)
	if err != nil {
		return applyPatchTransactionJournalForest{}, err
	}
	rollbackRoot, err := newApplyPatchTxnJournalLocation(intent.anchor, intent.rollbackRoot)
	if err != nil {
		return applyPatchTransactionJournalForest{}, err
	}
	sentinelWitness, err := newApplyPatchTxnJournalLocation(
		intent.anchor,
		intent.sentinelWitnessName,
	)
	if err != nil {
		return applyPatchTransactionJournalForest{}, err
	}
	forest := applyPatchTransactionJournalForest{
		ID:                   intent.id,
		PublicRoot:           publicRoot,
		StageRoot:            *stageRoot,
		RollbackRoot:         *rollbackRoot,
		SentinelRelativePath: intent.sentinelRelativePath,
		SentinelWitness:      *sentinelWitness,
		OperationIndexes:     make([]int, 0, len(intent.operations)),
	}
	entryByRelative := map[string]applyPatchTransactionJournalForestEntry{
		".": {
			RelativePath: ".", CanonicalPath: publicRoot,
			Kind: "directory",
		},
	}
	for _, operationIntent := range intent.operations {
		index := operationIntent.index
		if index < 0 || index >= len(operations) {
			return applyPatchTransactionJournalForest{}, errors.New(
				"apply-patch transaction forest operation is invalid",
			)
		}
		operation := operations[index]
		forest.OperationIndexes = append(forest.OperationIndexes, index)
		components := operationIntent.targetLayout.components[1:]
		for depth := 1; depth < len(components); depth++ {
			relative := filepath.ToSlash(filepath.Join(components[:depth]...))
			if _, exists := entryByRelative[relative]; !exists {
				entryByRelative[relative] = applyPatchTransactionJournalForestEntry{
					RelativePath: relative,
					CanonicalPath: filepath.Join(
						publicRoot,
						filepath.FromSlash(relative),
					),
					Kind: "directory",
				}
			}
		}
		relative := filepath.ToSlash(filepath.Join(components...))
		operationIndex := index
		entryByRelative[relative] = applyPatchTransactionJournalForestEntry{
			RelativePath:   relative,
			CanonicalPath:  operation.Target.CanonicalPath,
			Kind:           "file",
			OperationIndex: &operationIndex,
			Mode:           operation.After.Mode,
			Length:         operation.After.Length,
			SHA256:         operation.After.SHA256,
		}
	}
	forest.Entries = make([]applyPatchTransactionJournalForestEntry, 0, len(entryByRelative))
	for _, entry := range entryByRelative {
		forest.Entries = append(forest.Entries, entry)
	}
	sort.Slice(forest.Entries, func(left, right int) bool {
		if forest.Entries[left].RelativePath == forest.Entries[right].RelativePath {
			return false
		}
		if forest.Entries[left].RelativePath == "." {
			return true
		}
		if forest.Entries[right].RelativePath == "." {
			return false
		}
		return forest.Entries[left].RelativePath < forest.Entries[right].RelativePath
	})
	return forest, nil
}
