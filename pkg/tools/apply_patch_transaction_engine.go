package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

type applyPatchPreparedTransaction struct {
	state          *applyPatchTransactionState
	workspaceState *applyPatchTransactionWorkspaceState
	intent         *applyPatchTxnIntentPlan
	journal        *applyPatchTransactionJournal
	store          *applyPatchTxnStore
	key            [applyPatchTransactionAuthenticationBytes]byte
	effects        applyPatchTxnEffects
	fault          func(string) error
	closed         bool
}

func beginApplyPatchTransaction(
	ctx context.Context,
	state *applyPatchTransactionState,
	workspaceState *applyPatchTransactionWorkspaceState,
	plan *applyPatchPlan,
	probeFaults ...func(string) error,
) (*applyPatchPreparedTransaction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil || workspaceState == nil || plan == nil {
		return nil, errors.New("apply-patch transaction state is unavailable")
	}
	key, err := state.authenticationKey()
	if err != nil {
		return nil, err
	}
	intent, err := buildApplyPatchTxnIntent(ctx, plan)
	if err != nil {
		clear(key[:])
		return nil, err
	}
	transaction := &applyPatchPreparedTransaction{
		state: state, workspaceState: workspaceState, intent: intent, key: key,
	}
	fail := func(primary error) (*applyPatchPreparedTransaction, error) {
		cleanupErr := transaction.abortPreparing()
		return nil, errors.Join(primary, cleanupErr)
	}
	nameValidationErr := validateApplyPatchTxnIntentNamesAbsent(intent)
	if nameValidationErr != nil {
		return fail(nameValidationErr)
	}
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(plan.workspace)
	if err != nil {
		return fail(err)
	}
	rootPath, err := state.rootPath()
	if err != nil {
		return fail(err)
	}
	rootIdentity, err := state.rootIdentity()
	if err != nil {
		return fail(err)
	}
	workspaceRelative, err := workspaceState.directoryRelative()
	if err != nil {
		return fail(err)
	}
	stateBinding, err := newApplyPatchTxnStateBinding(
		rootPath,
		rootIdentity,
		key[:],
		workspaceRelative,
		intent,
	)
	if err != nil {
		return fail(err)
	}
	journal, err := newApplyPatchTxnPreparingJournal(
		key[:],
		workspaceBinding,
		stateBinding,
		intent,
	)
	if err != nil {
		return fail(err)
	}
	transaction.journal = journal
	removalValidationErr := validateApplyPatchTxnRemovalNamesAbsent(journal)
	if removalValidationErr != nil {
		return fail(removalValidationErr)
	}
	store, err := createApplyPatchTxnStore(workspaceState, intent)
	if err != nil {
		return fail(err)
	}
	transaction.store = store
	if err := store.writeJournal(key[:], journal); err != nil {
		return fail(err)
	}
	checkpoint := func(current *applyPatchTransactionJournal) error {
		return store.writeJournal(key[:], current)
	}
	if err := store.writeBackups(ctx, key[:], intent, journal, checkpoint); err != nil {
		return fail(err)
	}
	if err := stageApplyPatchTxnPostimages(ctx, intent, journal, checkpoint); err != nil {
		return fail(err)
	}
	var probeFault func(string) error
	if len(probeFaults) > 0 {
		probeFault = probeFaults[0]
	}
	if err := probeApplyPatchTxnSourceFallbackCapabilities(
		ctx,
		intent,
		journal,
		checkpoint,
		probeFault,
	); err != nil {
		return fail(err)
	}
	return transaction, nil
}

func validateApplyPatchTxnRemovalNamesAbsent(
	journal *applyPatchTransactionJournal,
) error {
	locations := make([]*applyPatchTransactionJournalRootedLocation, 0)
	for index := range journal.Artifacts {
		if journal.Artifacts[index].Rooted != nil {
			locations = append(locations, journal.Artifacts[index].Rooted)
		}
	}
	for index := range journal.Forests {
		forest := &journal.Forests[index]
		locations = append(
			locations,
			&forest.StageRoot,
			&forest.RollbackRoot,
			&forest.SentinelWitness,
		)
	}
	for _, location := range locations {
		anchor, err := openApplyPatchTxnAnchor(location.AnchorCanonicalPath)
		if err != nil || !anchor.identity.equal(location.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return errors.Join(
				errors.New("apply-patch transaction removal anchor changed"),
				err,
			)
		}
		_, _, inspectErr := applyPatchTxnIdentityAt(anchor, location.RemovalBasename)
		closeErr := anchor.Close()
		if closeErr != nil {
			return closeErr
		}
		if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		}
		return errors.Join(
			errors.New("apply-patch transaction removal name is not absent"),
			inspectErr,
		)
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) abortPreparing() error {
	if transaction == nil || transaction.closed {
		return nil
	}
	var cleanupErr error
	if transaction.store != nil && transaction.journal != nil && transaction.intent != nil {
		cleanupErr = cleanupApplyPatchTxnPrePONR(
			transaction.intent,
			transaction.journal,
			transaction.store,
			transaction.key[:],
		)
	}
	closeErr := transaction.closeHandles()
	if cleanupErr != nil {
		return errors.Join(
			errors.New("apply-patch transaction cleanup incomplete"),
			cleanupErr,
			closeErr,
		)
	}
	return closeErr
}

func (transaction *applyPatchPreparedTransaction) closeHandles() error {
	if transaction == nil || transaction.closed {
		return nil
	}
	transaction.closed = true
	var closeErr error
	if transaction.store != nil {
		closeErr = errors.Join(closeErr, transaction.store.Close())
		transaction.store = nil
	}
	if transaction.intent != nil {
		closeErr = errors.Join(closeErr, transaction.intent.Close())
		transaction.intent = nil
	}
	clear(transaction.key[:])
	return closeErr
}

func (transaction *applyPatchPreparedTransaction) revalidate(
	ctx context.Context,
	plan *applyPatchPlan,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if transaction == nil || transaction.closed || transaction.intent == nil ||
		transaction.journal == nil || transaction.store == nil || plan == nil ||
		transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		return errors.New("apply-patch transaction preparation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentKey, err := transaction.state.authenticationKey()
	if err != nil || !bytes.Equal(currentKey[:], transaction.key[:]) {
		clear(currentKey[:])
		return privateApplyPatchTxnError(errors.Join(
			errors.New("apply-patch transaction authentication state changed"),
			err,
		))
	}
	clear(currentKey[:])
	planErr := revalidateApplyPatchPlan(ctx, plan)
	if planErr != nil {
		return planErr
	}
	backupErr := transaction.store.verifyBackups(
		transaction.key[:],
		transaction.journal,
	)
	if backupErr != nil {
		return privateApplyPatchTxnError(backupErr)
	}
	publicStateErr := verifyApplyPatchTxnPreparingPublicState(
		transaction.intent,
		transaction.journal,
	)
	if publicStateErr != nil {
		return privateApplyPatchTxnError(publicStateErr)
	}
	probeErr := probeApplyPatchTxnNoReplaceCapabilities(
		transaction.intent,
		transaction.journal,
	)
	if probeErr != nil {
		return privateApplyPatchTxnError(probeErr)
	}
	if stateProbeErr := probeApplyPatchTxnStateNoReplaceCapability(
		transaction.workspaceState,
		transaction.store.activeName,
		transaction.store.activeInfo,
	); stateProbeErr != nil {
		return privateApplyPatchTxnError(stateProbeErr)
	}
	persisted, persistedBytes, err := transaction.store.readJournal(transaction.key[:])
	if err != nil {
		return privateApplyPatchTxnError(err)
	}
	encoded, err := encodeApplyPatchTransactionJournal(
		transaction.key[:],
		transaction.journal,
	)
	if err != nil || !bytes.Equal(encoded, persistedBytes) ||
		persisted.TransactionID != transaction.journal.TransactionID {
		return privateApplyPatchTxnError(errors.Join(
			errors.New("apply-patch transaction journal changed during preparation"),
			err,
		))
	}
	return ctx.Err()
}

func probeApplyPatchTxnStateNoReplaceCapability(
	workspace *applyPatchTransactionWorkspaceState,
	activeName string,
	activeInfo os.FileInfo,
) error {
	path, err := workspace.directoryPath()
	if err != nil {
		return err
	}
	anchor, err := openApplyPatchTxnAnchor(path)
	if err != nil {
		return err
	}
	defer anchor.Close()
	identity, err := applyPatchTxnIdentityFromFileInfo(activeInfo, "directory")
	if err != nil {
		return err
	}
	return applyPatchTxnProbeNoReplace(
		anchor,
		activeName,
		identity,
	)
}

func probeApplyPatchTxnNoReplaceCapabilities(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) error {
	seen := make(map[string]struct{})
	probe := func(
		anchor *applyPatchTxnAnchor,
		name string,
		identity applyPatchTxnIdentity,
	) error {
		key := anchor.canonical
		if _, exists := seen[key]; exists {
			return nil
		}
		seen[key] = struct{}{}
		return applyPatchTxnProbeNoReplace(anchor, name, identity)
	}
	for _, operation := range intent.operations {
		if operation.source != nil {
			if err := probe(
				operation.source.anchor,
				operation.source.basename,
				operation.source.state.Identity,
			); err != nil {
				return err
			}
		}
		if operation.targetAnchor != nil && operation.forest == nil {
			stage, err := requireApplyPatchTxnArtifact(
				journal,
				operation.index,
				applyPatchTransactionArtifactPostimageStage,
			)
			if err != nil || stage.Rooted.Identity == nil {
				return errors.Join(
					errors.New("apply-patch transaction capability probe stage is unavailable"),
					err,
				)
			}
			if err := probe(
				operation.targetAnchor,
				stage.Rooted.Basename,
				*stage.Rooted.Identity,
			); err != nil {
				return err
			}
		}
	}
	for _, forestIntent := range intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil || forest.StageRoot.Identity == nil {
			return errors.Join(
				errors.New("apply-patch transaction capability probe forest is unavailable"),
				err,
			)
		}
		if err := probe(
			forestIntent.anchor,
			forestIntent.stageRoot,
			*forest.StageRoot.Identity,
		); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) markPrepared(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if transaction == nil || transaction.closed || transaction.journal == nil ||
		transaction.store == nil ||
		transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		return errors.New("apply-patch transaction preparation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	transaction.journal.Phase = applyPatchTransactionPhasePrepared
	writeErr := transaction.store.writeJournal(transaction.key[:], transaction.journal)
	if writeErr == nil {
		return nil
	}
	persisted, _, readErr := transaction.store.readJournal(transaction.key[:])
	if readErr == nil && persisted.TransactionID == transaction.journal.TransactionID &&
		persisted.Phase == applyPatchTransactionPhasePrepared {
		transaction.store.mu.Lock()
		syncErr := syncApplyPatchTxnRootDirectory(transaction.store.activeRoot)
		transaction.store.mu.Unlock()
		if syncErr == nil {
			return nil
		}
		readErr = syncErr
	}
	transaction.journal.Phase = applyPatchTransactionPhasePreparing
	return errors.Join(writeErr, readErr)
}

func verifyApplyPatchTxnPreparingPublicState(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) error {
	for _, operation := range intent.operations {
		if operation.source != nil {
			if err := operation.source.anchor.revalidate(); err != nil {
				return err
			}
			sourceState, err := verifyApplyPatchTxnRegular(
				operation.source.anchor,
				operation.source.basename,
				journal.Operations[operation.index].Before,
				journal.Operations[operation.index].Source.PreflightLinks,
			)
			if err != nil || !sourceState.Identity.equal(operation.source.state.Identity) {
				return errors.Join(
					fmt.Errorf("patch source %q changed during transaction preparation", operation.planned.sourceLabel),
					err,
				)
			}
			for _, role := range []applyPatchTransactionArtifactRole{
				applyPatchTransactionArtifactSourceRestoreStage,
				applyPatchTransactionArtifactSourceProbeWitness,
				applyPatchTransactionArtifactSourceWitness,
				applyPatchTransactionArtifactSourceQuarantine,
			} {
				artifact, err := requireApplyPatchTxnArtifact(journal, operation.index, role)
				if err != nil {
					return err
				}
				if err := requireApplyPatchTxnRootedAbsent(
					operation.source.anchor,
					artifact.Rooted,
				); err != nil {
					return err
				}
			}
		}
		if operation.targetAnchor == nil || operation.forest != nil {
			continue
		}
		if err := operation.targetAnchor.revalidate(); err != nil {
			return err
		}
		stage, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil || stage.Rooted.Identity == nil {
			return errors.Join(errors.New("apply-patch transaction stage is unavailable"), err)
		}
		state, err := verifyApplyPatchTxnRegular(
			operation.targetAnchor,
			stage.Rooted.Basename,
			stage.Expected,
			stage.Rooted.Links,
		)
		if err != nil || !state.Identity.equal(*stage.Rooted.Identity) {
			return errors.Join(errors.New("apply-patch transaction stage changed"), err)
		}
		witness, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil || witness.Rooted.Identity == nil {
			return errors.Join(errors.New("apply-patch transaction witness is unavailable"), err)
		}
		state, err = verifyApplyPatchTxnRegular(
			operation.targetAnchor,
			witness.Rooted.Basename,
			witness.Expected,
			witness.Rooted.Links,
		)
		if err != nil || !state.Identity.equal(*witness.Rooted.Identity) {
			return errors.Join(errors.New("apply-patch transaction witness changed"), err)
		}
		rollback, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactTargetRollbackQuarantine,
		)
		if err != nil {
			return err
		}
		if err := requireApplyPatchTxnAbsent(
			operation.targetAnchor,
			rollback.Rooted.Basename,
		); err != nil {
			return err
		}
		if operation.planned.kind != "update" {
			if err := requireApplyPatchTxnAbsent(
				operation.targetAnchor,
				operation.targetLayout.components[0],
			); err != nil {
				return fmt.Errorf(
					"patch target %q changed during transaction preparation: %w",
					operation.planned.targetLabel,
					err,
				)
			}
		}
	}
	for _, forestIntent := range intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil {
			return err
		}
		if err := verifyApplyPatchTxnStagedForest(forestIntent, forest); err != nil {
			return err
		}
	}
	return validateApplyPatchTxnPreEffectDeclaredNames(intent, journal)
}

func verifyApplyPatchTxnStagedForest(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
) error {
	if err := intent.anchor.revalidate(); err != nil {
		return err
	}
	if err := requireApplyPatchTxnAbsent(intent.anchor, intent.publicRoot); err != nil {
		return err
	}
	if err := requireApplyPatchTxnAbsent(intent.anchor, intent.rollbackRoot); err != nil {
		return err
	}
	if forest.StageRoot.Identity == nil || forest.SentinelWitness.Identity == nil {
		return errors.New("apply-patch transaction forest is not checkpointed")
	}
	rootState, err := applyPatchTxnInspectAt(intent.anchor, intent.stageRoot)
	if err != nil || !rootState.Identity.equal(*forest.StageRoot.Identity) ||
		uint32(rootState.Mode.Perm()) != forest.Entries[0].Mode {
		return errors.Join(errors.New("apply-patch transaction forest root changed"), err)
	}
	stageRootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
	for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
		entry := &forest.Entries[entryIndex]
		if entry.Identity == nil {
			return errors.New("apply-patch transaction forest entry is not checkpointed")
		}
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
			state, inspectErr := applyPatchTxnInspectAt(parent, basename)
			_ = parent.Close()
			if inspectErr != nil || !state.Identity.equal(*entry.Identity) ||
				uint32(state.Mode.Perm()) != entry.Mode {
				return errors.Join(
					errors.New("apply-patch transaction forest directory changed"),
					inspectErr,
				)
			}
			continue
		}
		state, inspectErr := verifyApplyPatchTxnRegular(
			parent,
			basename,
			mapApplyPatchTxnForestEntryState(*entry),
			entry.Links,
		)
		_ = parent.Close()
		if inspectErr != nil || !state.Identity.equal(*entry.Identity) {
			return errors.Join(
				errors.New("apply-patch transaction forest file changed"),
				inspectErr,
			)
		}
	}
	sentinel := findApplyPatchTxnForestSentinel(forest)
	if sentinel == nil {
		return errors.New("apply-patch transaction forest sentinel is unavailable")
	}
	witnessState, err := verifyApplyPatchTxnRegular(
		intent.anchor,
		intent.sentinelWitnessName,
		mapApplyPatchTxnForestEntryState(*sentinel),
		forest.SentinelWitness.Links,
	)
	if err != nil || !witnessState.Identity.equal(*forest.SentinelWitness.Identity) {
		return errors.Join(errors.New("apply-patch transaction forest witness changed"), err)
	}
	return verifyApplyPatchTxnForestManifestAt(intent, forest, intent.stageRoot)
}

func verifyApplyPatchTxnForestManifestAt(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
	rootName string,
) error {
	if intent == nil || forest == nil || rootName == "" {
		return errors.New("apply-patch transaction forest manifest is unavailable")
	}
	expected := expectedApplyPatchTxnForestChildren(forest)
	directories := make([]string, 0, len(expected))
	for relative := range expected {
		directories = append(directories, relative)
	}
	sort.Strings(directories)
	rootPath := filepath.Join(intent.anchorPath, rootName)
	for _, relative := range directories {
		path := rootPath
		if relative != "." {
			path = filepath.Join(rootPath, filepath.FromSlash(relative))
		}
		anchor, err := openApplyPatchTxnAnchor(path)
		if err != nil {
			return err
		}
		wanted := append([]string(nil), expected[relative]...)
		sort.Strings(wanted)
		actual, readErr := applyPatchTxnReadDirectoryNames(anchor, len(wanted))
		closeErr := anchor.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if !slices.Equal(actual, wanted) {
			return errors.New("apply-patch transaction forest manifest has an alien entry")
		}
	}
	return nil
}

func expectedApplyPatchTxnForestChildren(
	forest *applyPatchTransactionJournalForest,
) map[string][]string {
	expected := map[string][]string{".": {}}
	if forest == nil {
		return expected
	}
	for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
		entry := forest.Entries[entryIndex]
		relative := filepath.FromSlash(entry.RelativePath)
		parent := filepath.ToSlash(filepath.Dir(relative))
		if parent == "" {
			parent = "."
		}
		expected[parent] = append(expected[parent], filepath.Base(relative))
		if entry.Kind == "directory" {
			expected[entry.RelativePath] = append([]string(nil), expected[entry.RelativePath]...)
		}
	}
	return expected
}

func findApplyPatchTxnForestSentinel(
	forest *applyPatchTransactionJournalForest,
) *applyPatchTransactionJournalForestEntry {
	if forest == nil {
		return nil
	}
	for index := range forest.Entries {
		if forest.Entries[index].RelativePath == forest.SentinelRelativePath {
			return &forest.Entries[index]
		}
	}
	return nil
}

func requireApplyPatchTxnAbsent(anchor *applyPatchTxnAnchor, basename string) error {
	_, _, err := applyPatchTxnIdentityAt(anchor, basename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(errors.New("apply-patch transaction expected an absent path"), err)
}
