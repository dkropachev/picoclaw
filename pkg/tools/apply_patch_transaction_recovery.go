package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (t *ApplyPatchTool) recoverApplyPatchTransaction(
	ctx context.Context,
	state *applyPatchTransactionState,
	workspaceState *applyPatchTransactionWorkspaceState,
	workspace applyPatchWorkspace,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := state.authenticationKey()
	if err != nil {
		return err
	}
	defer clear(key[:])
	store, journal, err := openApplyPatchTxnRecoveryStore(
		workspaceState,
		key[:],
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	bindingErr := validateApplyPatchTxnRecoveryBindings(
		state,
		workspaceState,
		workspace,
		key[:],
		journal,
	)
	if bindingErr != nil {
		_ = store.Close()
		return bindingErr
	}
	authorizationErr := t.authorizeApplyPatchTxnRecovery(ctx, workspace, journal)
	if authorizationErr != nil {
		_ = store.Close()
		return authorizationErr
	}
	intent, err := reconstructApplyPatchTxnIntent(journal)
	if err != nil {
		_ = store.Close()
		return err
	}
	transaction := &applyPatchPreparedTransaction{
		state: state, workspaceState: workspaceState, intent: intent,
		journal: journal, store: store, key: key, fault: t.transactionFault,
	}
	// The transaction owns its detached key copy from here.
	clear(key[:])
	if err := preflightApplyPatchTxnRecoveryMutation(transaction); err != nil {
		_ = transaction.closeHandles()
		return fmt.Errorf("apply-patch recovery pre-mutation validation: %w", err)
	}
	if journal.Phase == applyPatchTransactionPhaseCommitted {
		if err := transaction.resyncVisibleCommittedDecision(); err != nil {
			_ = transaction.closeHandles()
			return err
		}
	}
	if err := transaction.store.finishRecoveryJournalStage(); err != nil {
		_ = transaction.closeHandles()
		return err
	}
	if err := reconcileApplyPatchTxnRemovalQuarantines(transaction); err != nil {
		_ = transaction.closeHandles()
		return fmt.Errorf("apply-patch recovery rooted removal reconciliation: %w", err)
	}
	if err := classifyApplyPatchTxnRecovery(transaction); err != nil {
		_ = transaction.closeHandles()
		return fmt.Errorf("apply-patch recovery state classification: %w", err)
	}
	if err := reconcileApplyPatchTxnForestEntryRemovalQuarantines(transaction); err != nil {
		_ = transaction.closeHandles()
		return fmt.Errorf("apply-patch recovery forest removal reconciliation: %w", err)
	}
	if err := transaction.store.verifyRecoveryBackups(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		_ = transaction.closeHandles()
		return err
	}
	if err := validateApplyPatchTxnRecoveryParticipants(transaction); err != nil {
		_ = transaction.closeHandles()
		return err
	}
	switch journal.Phase {
	case applyPatchTransactionPhasePreparing:
		return transaction.abortPreparing()
	case applyPatchTransactionPhasePrepared,
		applyPatchTransactionPhaseRollingBack:
		primary := errors.New("recover interrupted apply-patch transaction")
		recoveryErr := transaction.rollback(primary)
		if errors.Is(recoveryErr, primary) &&
			!errors.Is(recoveryErr, errApplyPatchRollbackIncomplete) {
			return nil
		}
		return recoveryErr
	case applyPatchTransactionPhaseCommitted:
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent,
			transaction.journal,
			transaction.effects,
			transaction.store.committedCleanupAuthenticated,
		); err != nil {
			_ = transaction.closeHandles()
			return fmt.Errorf("apply-patch committed recovery conflict: %w", err)
		}
		if err := transaction.cleanupCommitted(); err != nil {
			_ = transaction.closeHandles()
			return fmt.Errorf("apply-patch committed cleanup incomplete: %w", err)
		}
		return transaction.closeHandles()
	default:
		_ = transaction.closeHandles()
		return errors.New("apply-patch transaction recovery phase is invalid")
	}
}

// preflightApplyPatchTxnRecoveryMutation validates one virtual view in which an
// interrupted exact removal is still addressed by its authenticated removal
// basename. It performs no filesystem mutation. Recovery may reconcile those
// basenames only after every public endpoint, private participant, forest, and
// backup has passed this whole-transaction pass.
func preflightApplyPatchTxnRecoveryMutation(
	transaction *applyPatchPreparedTransaction,
) error {
	if transaction == nil || transaction.journal == nil || transaction.store == nil {
		return errors.New("apply-patch recovery preflight is unavailable")
	}
	encoded, err := encodeApplyPatchTransactionJournal(
		transaction.key[:],
		transaction.journal,
	)
	if err != nil {
		return err
	}
	virtualJournal, err := decodeApplyPatchTransactionJournal(
		transaction.key[:],
		encoded,
	)
	if err != nil {
		return err
	}
	for index := range virtualJournal.Artifacts {
		location := virtualJournal.Artifacts[index].Rooted
		if location == nil {
			continue
		}
		name, _, inspectErr := inspectApplyPatchTxnVirtualRootedRemoval(
			location,
			"regular",
		)
		if inspectErr != nil {
			return fmt.Errorf(
				"apply-patch recovery artifact %s removal view: %w",
				virtualJournal.Artifacts[index].Role,
				inspectErr,
			)
		}
		location.Basename = name
	}
	for index := range virtualJournal.Forests {
		forest := &virtualJournal.Forests[index]
		for label, participant := range map[string]struct {
			location *applyPatchTransactionJournalRootedLocation
			kind     string
		}{
			"stage root":       {location: &forest.StageRoot, kind: "directory"},
			"rollback root":    {location: &forest.RollbackRoot, kind: "directory"},
			"sentinel witness": {location: &forest.SentinelWitness, kind: "regular"},
		} {
			location := participant.location
			name, _, inspectErr := inspectApplyPatchTxnVirtualRootedRemoval(
				location,
				participant.kind,
			)
			if inspectErr != nil {
				return fmt.Errorf(
					"apply-patch recovery forest %s removal view: %w",
					label,
					inspectErr,
				)
			}
			location.Basename = name
		}
	}
	virtualIntent, err := reconstructApplyPatchTxnIntent(virtualJournal)
	if err != nil {
		return err
	}
	defer virtualIntent.Close()
	if err := applyApplyPatchTxnVirtualForestEntryNames(
		virtualIntent,
		virtualJournal,
	); err != nil {
		return err
	}
	virtualTransaction := &applyPatchPreparedTransaction{
		state: transaction.state, workspaceState: transaction.workspaceState,
		intent: virtualIntent, journal: virtualJournal, store: transaction.store,
		key: transaction.key,
	}
	if err := classifyApplyPatchTxnRecovery(virtualTransaction); err != nil {
		return err
	}
	if err := transaction.store.verifyRecoveryBackups(
		transaction.key[:],
		virtualJournal,
	); err != nil {
		return err
	}
	if err := validateApplyPatchTxnVirtualRootedArtifacts(virtualTransaction); err != nil {
		return err
	}
	if err := validateApplyPatchTxnRecoveryParticipants(virtualTransaction); err != nil {
		return err
	}
	return validateApplyPatchTxnPreparingVirtualForests(virtualTransaction)
}

func validateApplyPatchTxnVirtualRootedArtifacts(
	transaction *applyPatchPreparedTransaction,
) error {
	aliases, err := collectApplyPatchTxnVirtualRegularAliases(
		transaction.intent,
		transaction.journal,
	)
	if err != nil {
		return err
	}
	for index := range transaction.journal.Artifacts {
		artifact := &transaction.journal.Artifacts[index]
		if artifact.Rooted == nil || artifact.Rooted.Identity == nil {
			continue
		}
		anchor, err := openApplyPatchTxnAnchor(artifact.Rooted.AnchorCanonicalPath)
		if err != nil || !anchor.identity.equal(artifact.Rooted.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return errors.Join(
				errors.New("apply-patch recovery artifact anchor changed"),
				err,
			)
		}
		state, inspectErr := applyPatchTxnInspectAt(anchor, artifact.Rooted.Basename)
		if errors.Is(inspectErr, os.ErrNotExist) {
			_ = anchor.Close()
			continue
		}
		if inspectErr != nil || !state.Identity.equal(*artifact.Rooted.Identity) {
			_ = anchor.Close()
			return errors.Join(
				errors.New("apply-patch recovery rooted artifact identity conflict"),
				inspectErr,
			)
		}
		expectedLinks := uint64(len(aliases[*artifact.Rooted.Identity]))
		if expectedLinks == 0 || state.Links != expectedLinks {
			_ = anchor.Close()
			return errors.New("apply-patch recovery rooted artifact link conflict")
		}
		if artifact.Role == applyPatchTransactionArtifactSourceRestoreStage {
			if transaction.journal.Phase == applyPatchTransactionPhasePreparing &&
				artifact.Rooted.Links == 1 {
				// Identity-checkpointed, exclusively created private stages are
				// deletion-owned during preparing recovery. They are never resumed
				// or published without authenticated backup-prefix validation.
				_ = anchor.Close()
				continue
			}
			validationErr := validateApplyPatchTxnVirtualRestoreStage(
				transaction,
				anchor,
				artifact,
				expectedLinks,
			)
			_ = anchor.Close()
			if validationErr != nil {
				return validationErr
			}
			continue
		}
		if transaction.journal.Phase == applyPatchTransactionPhasePreparing &&
			artifact.Role == applyPatchTransactionArtifactPostimageStage &&
			artifact.Rooted.Links == 1 {
			// As above, the identity checkpoint authorizes deletion only. A
			// completed or hardlink-witnessed stage still takes the exact
			// digest/mode/link validation path below.
			_ = anchor.Close()
			continue
		}
		verified, verifyErr := verifyApplyPatchTxnRegular(
			anchor,
			artifact.Rooted.Basename,
			artifact.Expected,
			expectedLinks,
		)
		_ = anchor.Close()
		if verifyErr != nil || !verified.Identity.equal(*artifact.Rooted.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery rooted artifact content conflict"),
				verifyErr,
			)
		}
	}
	return validateApplyPatchTxnVirtualForestWitnesses(transaction, aliases)
}

func validateApplyPatchTxnVirtualRestoreStage(
	transaction *applyPatchPreparedTransaction,
	anchor *applyPatchTxnAnchor,
	artifact *applyPatchTransactionJournalArtifact,
	expectedLinks uint64,
) error {
	backup, err := transaction.store.readBackup(
		transaction.key[:],
		transaction.journal,
		artifact.OperationIndex,
	)
	if err != nil {
		return err
	}
	data, mode, identity, err := applyPatchTxnReadRegular(
		anchor,
		artifact.Rooted.Basename,
		int64(artifact.Expected.Length),
	)
	if err != nil || !identity.equal(*artifact.Rooted.Identity) ||
		len(data) > len(backup) || !bytes.Equal(data, backup[:len(data)]) {
		return errors.Join(
			errors.New("apply-patch recovery restore stage content conflict"),
			err,
		)
	}
	state, inspectErr := applyPatchTxnInspectAt(anchor, artifact.Rooted.Basename)
	if inspectErr != nil || state.Links != expectedLinks {
		return errors.Join(
			errors.New("apply-patch recovery restore stage link conflict"),
			inspectErr,
		)
	}
	expectedMode := os.FileMode(artifact.Expected.Mode).Perm()
	if mode.Perm() != 0o600 &&
		(len(data) != len(backup) || mode.Perm() != expectedMode) {
		return errors.New("apply-patch recovery restore stage mode conflict")
	}
	return nil
}

func collectApplyPatchTxnVirtualRegularAliases(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) (map[applyPatchTxnIdentity]map[string]struct{}, error) {
	aliases := make(map[applyPatchTxnIdentity]map[string]struct{})
	add := func(anchor *applyPatchTxnAnchor, name string, path string) error {
		state, err := applyPatchTxnInspectAt(anchor, name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if state.Identity.Kind != "regular" {
			return nil
		}
		paths := aliases[state.Identity]
		if paths == nil {
			paths = make(map[string]struct{})
			aliases[state.Identity] = paths
		}
		paths[path] = struct{}{}
		return nil
	}
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Rooted == nil || artifact.Rooted.Identity == nil {
			continue
		}
		anchor, err := openApplyPatchTxnAnchor(artifact.Rooted.AnchorCanonicalPath)
		if err != nil || !anchor.identity.equal(artifact.Rooted.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return nil, errors.Join(
				errors.New("apply-patch recovery alias anchor changed"),
				err,
			)
		}
		addErr := add(
			anchor,
			artifact.Rooted.Basename,
			filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename),
		)
		closeErr := anchor.Close()
		if addErr != nil || closeErr != nil {
			return nil, errors.Join(addErr, closeErr)
		}
	}
	for index := range journal.Forests {
		forest := &journal.Forests[index]
		if forest.SentinelWitness.Identity == nil {
			continue
		}
		forestIntent := intent.forests[index]
		if err := add(
			forestIntent.anchor,
			forest.SentinelWitness.Basename,
			filepath.Join(forestIntent.anchorPath, forest.SentinelWitness.Basename),
		); err != nil {
			return nil, err
		}
		rootName, err := selectApplyPatchTxnVirtualForestRoot(forestIntent, forest)
		if err != nil {
			return nil, err
		}
		if rootName == "" {
			continue
		}
		rootPath := filepath.Join(forestIntent.anchorPath, rootName)
		for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
			entry := &forest.Entries[entryIndex]
			if entry.Kind != "file" || entry.Identity == nil {
				continue
			}
			parentPath := filepath.Join(
				rootPath,
				filepath.Dir(filepath.FromSlash(entry.RelativePath)),
			)
			parent, openErr := openApplyPatchTxnAnchor(parentPath)
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			if openErr != nil {
				return nil, openErr
			}
			basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
			addErr := add(parent, basename, filepath.Join(parentPath, basename))
			closeErr := parent.Close()
			if addErr != nil || closeErr != nil {
				return nil, errors.Join(addErr, closeErr)
			}
		}
	}
	for _, operation := range intent.operations {
		if operation.source != nil {
			if err := add(
				operation.source.anchor,
				operation.source.basename,
				operation.planned.sourcePath,
			); err != nil {
				return nil, err
			}
		}
		if operation.targetAnchor != nil && operation.forest == nil {
			if err := add(
				operation.targetAnchor,
				operation.targetLayout.components[0],
				operation.planned.targetPath,
			); err != nil {
				return nil, err
			}
		}
	}
	return aliases, nil
}

func validateApplyPatchTxnVirtualForestWitnesses(
	transaction *applyPatchPreparedTransaction,
	aliases map[applyPatchTxnIdentity]map[string]struct{},
) error {
	for index := range transaction.journal.Forests {
		forest := &transaction.journal.Forests[index]
		if forest.SentinelWitness.Identity == nil {
			continue
		}
		intent := transaction.intent.forests[index]
		present, err := applyPatchTxnRecoveryIdentityPresent(
			intent.anchor,
			forest.SentinelWitness.Basename,
			*forest.SentinelWitness.Identity,
		)
		if err != nil || !present {
			continue
		}
		sentinel := findApplyPatchTxnForestSentinel(forest)
		if sentinel == nil {
			return errors.New("apply-patch recovery forest sentinel is unavailable")
		}
		expectedLinks := uint64(len(aliases[*forest.SentinelWitness.Identity]))
		state, verifyErr := verifyApplyPatchTxnRegular(
			intent.anchor,
			forest.SentinelWitness.Basename,
			mapApplyPatchTxnForestEntryState(*sentinel),
			expectedLinks,
		)
		if verifyErr != nil || !state.Identity.equal(*forest.SentinelWitness.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery forest witness conflict"),
				verifyErr,
			)
		}
	}
	return nil
}

func inspectApplyPatchTxnVirtualRootedRemoval(
	location *applyPatchTransactionJournalRootedLocation,
	kind string,
) (string, bool, error) {
	if location == nil {
		return "", false, errors.New("apply-patch recovery rooted participant is unavailable")
	}
	anchor, err := openApplyPatchTxnAnchor(location.AnchorCanonicalPath)
	if err != nil || !anchor.identity.equal(location.AnchorIdentity) {
		if anchor != nil {
			_ = anchor.Close()
		}
		return "", false, errors.Join(
			errors.New("apply-patch recovery removal anchor changed"),
			err,
		)
	}
	defer anchor.Close()
	return inspectApplyPatchTxnVirtualRemovalAt(anchor, location, kind)
}

func inspectApplyPatchTxnVirtualRemovalAt(
	anchor *applyPatchTxnAnchor,
	location *applyPatchTransactionJournalRootedLocation,
	kind string,
) (string, bool, error) {
	if location.Identity == nil {
		if location.RemovalAttempted {
			return "", false, errors.New("apply-patch uncheckpointed removal was attempted")
		}
		if _, _, err := applyPatchTxnIdentityAt(anchor, location.RemovalBasename); !errors.Is(err, os.ErrNotExist) {
			return "", false, errors.Join(
				errors.New("apply-patch unexpected removal quarantine"),
				err,
			)
		}
		return location.Basename, false, nil
	}
	inspect := func(name string) (bool, error) {
		identity, _, inspectErr := applyPatchTxnIdentityAt(anchor, name)
		if errors.Is(inspectErr, os.ErrNotExist) {
			return false, nil
		}
		if inspectErr != nil || !identity.equal(*location.Identity) || identity.Kind != kind {
			return false, errors.Join(
				errors.New("apply-patch recovery removal participant conflict"),
				inspectErr,
			)
		}
		return true, nil
	}
	basenamePresent, err := inspect(location.Basename)
	if err != nil {
		return "", false, err
	}
	removalPresent, err := inspect(location.RemovalBasename)
	if err != nil {
		return "", false, err
	}
	if !location.RemovalAttempted {
		if removalPresent {
			return "", false, errors.New("apply-patch unexpected removal quarantine")
		}
		return location.Basename, basenamePresent, nil
	}
	if basenamePresent && removalPresent {
		return "", false, errors.New("apply-patch recovery removal state is ambiguous")
	}
	if removalPresent {
		return location.RemovalBasename, true, nil
	}
	return location.Basename, basenamePresent, nil
}

func applyApplyPatchTxnVirtualForestEntryNames(
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
) error {
	for _, forestIntent := range intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(journal, forestIntent.id)
		if err != nil || forest.StageRoot.Identity == nil {
			continue
		}
		rootName, err := selectApplyPatchTxnVirtualForestRoot(forestIntent, forest)
		if err != nil {
			return err
		}
		if rootName == "" {
			continue
		}
		rootPath := filepath.Join(forestIntent.anchorPath, rootName)
		actualDirectories := map[string]string{".": "."}
		for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
			entry := &forest.Entries[entryIndex]
			originalRelative := filepath.ToSlash(entry.RelativePath)
			originalParent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(originalRelative)))
			if originalParent == "" {
				originalParent = "."
			}
			actualParent, parentKnown := actualDirectories[originalParent]
			if !parentKnown {
				continue
			}
			parentPath := rootPath
			if actualParent != "." {
				parentPath = filepath.Join(rootPath, filepath.FromSlash(actualParent))
			}
			parent, openErr := openApplyPatchTxnAnchor(parentPath)
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			if openErr != nil {
				return openErr
			}
			location := &applyPatchTransactionJournalRootedLocation{
				Basename:         filepath.Base(filepath.FromSlash(originalRelative)),
				RemovalBasename:  entry.RemovalBasename,
				RemovalAttempted: entry.RemovalAttempted,
				Identity:         entry.Identity,
			}
			if entry.Identity == nil {
				_, _, uncheckpointedErr := applyPatchTxnIdentityAt(
					parent,
					location.Basename,
				)
				if uncheckpointedErr == nil {
					_ = parent.Close()
					return errors.New(
						"apply-patch recovery forest entry was created before its identity checkpoint",
					)
				}
				if !errors.Is(uncheckpointedErr, os.ErrNotExist) {
					_ = parent.Close()
					return uncheckpointedErr
				}
			}
			identityKind := "regular"
			if entry.Kind == "directory" {
				identityKind = "directory"
			}
			actualName, present, inspectErr := inspectApplyPatchTxnVirtualRemovalAt(
				parent,
				location,
				identityKind,
			)
			closeErr := parent.Close()
			if inspectErr != nil || closeErr != nil {
				return errors.Join(
					fmt.Errorf(
						"apply-patch recovery forest entry %s removal view: %w",
						entry.Kind,
						inspectErr,
					),
					closeErr,
				)
			}
			actualRelative := actualName
			if actualParent != "." {
				actualRelative = filepath.ToSlash(filepath.Join(actualParent, actualName))
			}
			entry.RelativePath = filepath.ToSlash(actualRelative)
			if forest.SentinelRelativePath == originalRelative {
				forest.SentinelRelativePath = entry.RelativePath
			}
			if entry.Kind == "directory" && present {
				actualDirectories[originalRelative] = entry.RelativePath
			}
		}
	}
	return nil
}

func selectApplyPatchTxnVirtualForestRoot(
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
) (string, error) {
	type candidate struct {
		name     string
		expected *applyPatchTxnIdentity
	}
	rollbackIdentity := forest.RollbackRoot.Identity
	if rollbackIdentity == nil {
		rollbackIdentity = forest.StageRoot.Identity
	}
	candidates := []candidate{
		{name: intent.stageRoot, expected: forest.StageRoot.Identity},
		{name: intent.publicRoot, expected: forest.StageRoot.Identity},
		{name: intent.rollbackRoot, expected: rollbackIdentity},
	}
	found := ""
	for _, candidate := range candidates {
		if candidate.expected == nil {
			continue
		}
		present, err := applyPatchTxnRecoveryIdentityPresent(
			intent.anchor,
			candidate.name,
			*candidate.expected,
		)
		if err != nil {
			return "", err
		}
		if !present {
			continue
		}
		if found != "" {
			return "", errors.New("apply-patch recovery forest state is ambiguous")
		}
		found = candidate.name
	}
	return found, nil
}

func validateApplyPatchTxnPreparingVirtualForests(
	transaction *applyPatchPreparedTransaction,
) error {
	if transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		return nil
	}
	aliases, err := collectApplyPatchTxnVirtualRegularAliases(
		transaction.intent,
		transaction.journal,
	)
	if err != nil {
		return err
	}
	for _, intent := range transaction.intent.forests {
		forest, forestErr := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
		if forestErr != nil {
			return forestErr
		}
		if forest.StageRoot.Identity == nil {
			continue
		}
		present, presenceErr := applyPatchTxnRecoveryIdentityPresent(
			intent.anchor,
			intent.stageRoot,
			*forest.StageRoot.Identity,
		)
		if presenceErr != nil {
			return presenceErr
		}
		if !present {
			continue
		}
		rootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
		for entryIndex := 1; entryIndex < len(forest.Entries); entryIndex++ {
			entry := &forest.Entries[entryIndex]
			if entry.Identity == nil || entry.Kind != "file" {
				continue
			}
			parentPath := filepath.Join(
				rootPath,
				filepath.Dir(filepath.FromSlash(entry.RelativePath)),
			)
			parent, openErr := openApplyPatchTxnAnchor(parentPath)
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			if openErr != nil {
				return openErr
			}
			basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
			state, inspectErr := applyPatchTxnInspectAt(parent, basename)
			closeErr := parent.Close()
			if errors.Is(inspectErr, os.ErrNotExist) {
				continue
			}
			expectedLinks := uint64(len(aliases[*entry.Identity]))
			if inspectErr != nil || closeErr != nil ||
				!state.Identity.equal(*entry.Identity) || state.Links != expectedLinks {
				return errors.Join(
					errors.New("apply-patch recovery partial forest entry ownership conflict"),
					inspectErr,
					closeErr,
				)
			}
		}
		expectedChildren := expectedApplyPatchTxnForestChildren(forest)
		for relative, wanted := range expectedChildren {
			directoryPath := rootPath
			if relative != "." {
				directoryPath = filepath.Join(rootPath, filepath.FromSlash(relative))
			}
			directory, openErr := openApplyPatchTxnAnchor(directoryPath)
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			if openErr != nil {
				return openErr
			}
			actual, readErr := applyPatchTxnReadDirectoryNames(directory, len(wanted))
			closeErr := directory.Close()
			if readErr != nil || closeErr != nil {
				return errors.Join(readErr, closeErr)
			}
			allowed := make(map[string]struct{}, len(wanted))
			for _, name := range wanted {
				allowed[name] = struct{}{}
			}
			for _, name := range actual {
				if _, ok := allowed[name]; !ok {
					return errors.New("apply-patch recovery partial forest has an alien entry")
				}
			}
		}
		if forest.SentinelWitness.Identity == nil {
			continue
		}
		if err := verifyApplyPatchTxnRollingForestTreeAt(
			intent,
			forest,
			intent.stageRoot,
		); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *applyPatchPreparedTransaction) resyncVisibleCommittedDecision() error {
	if transaction == nil || transaction.store == nil || transaction.journal == nil ||
		transaction.journal.Phase != applyPatchTransactionPhaseCommitted ||
		!transaction.journal.DecisionAttempted {
		return errors.Join(
			errApplyPatchCommitUncertain,
			errors.New("apply-patch visible committed decision is invalid"),
		)
	}
	if err := transaction.injectFault("committed_recovery_visible_before_sync"); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, err)
	}
	if err := transaction.injectFault("committed_recovery_journal_sync"); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, err)
	}
	transaction.store.mu.Lock()
	defer transaction.store.mu.Unlock()
	if err := transaction.store.revalidateLocked(); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, err)
	}
	if err := transaction.store.revalidateCurrentJournalLocked(transaction.key[:]); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, err)
	}
	if err := syncApplyPatchTxnRootDirectory(transaction.store.activeRoot); err != nil {
		return errors.Join(errApplyPatchCommitUncertain, err)
	}
	return nil
}

func reconcileApplyPatchTxnRemovalQuarantines(
	transaction *applyPatchPreparedTransaction,
) error {
	for index := range transaction.journal.Artifacts {
		artifact := &transaction.journal.Artifacts[index]
		if artifact.Rooted == nil {
			continue
		}
		if err := reconcileApplyPatchTxnRootedRemoval(
			transaction,
			artifact.Rooted,
			"regular",
		); err != nil {
			return err
		}
	}
	for index := range transaction.journal.Forests {
		forest := &transaction.journal.Forests[index]
		for location, kind := range map[*applyPatchTransactionJournalRootedLocation]string{
			&forest.StageRoot:       "directory",
			&forest.RollbackRoot:    "directory",
			&forest.SentinelWitness: "regular",
		} {
			if err := reconcileApplyPatchTxnRootedRemoval(
				transaction,
				location,
				kind,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func reconcileApplyPatchTxnRootedRemoval(
	transaction *applyPatchPreparedTransaction,
	location *applyPatchTransactionJournalRootedLocation,
	kind string,
) error {
	anchor, err := openApplyPatchTxnAnchor(location.AnchorCanonicalPath)
	if err != nil || !anchor.identity.equal(location.AnchorIdentity) {
		if anchor != nil {
			_ = anchor.Close()
		}
		return errors.Join(
			errors.New("apply-patch removal anchor changed"),
			err,
		)
	}
	defer anchor.Close()
	removalIdentity, _, inspectErr := applyPatchTxnIdentityAt(
		anchor,
		location.RemovalBasename,
	)
	if errors.Is(inspectErr, os.ErrNotExist) {
		if location.RemovalAttempted {
			if syncErr := applyPatchTxnSyncDirectory(anchor); syncErr != nil {
				return syncErr
			}
			location.RemovalAttempted = false
			return transaction.checkpoint()
		}
		return nil
	}
	if !location.RemovalAttempted {
		return errors.New("apply-patch unexpected removal quarantine")
	}
	if inspectErr != nil || location.Identity == nil ||
		!removalIdentity.equal(*location.Identity) {
		return errors.Join(
			errors.New("apply-patch removal quarantine conflict"),
			inspectErr,
		)
	}
	if err := applyPatchTxnRemoveExact(
		anchor,
		location.Basename,
		location.RemovalBasename,
		*location.Identity,
		kind == "directory",
	); err != nil {
		return err
	}
	if err := applyPatchTxnSyncDirectory(anchor); err != nil {
		return err
	}
	location.RemovalAttempted = false
	return transaction.checkpoint()
}

func reconcileApplyPatchTxnForestEntryRemovalQuarantines(
	transaction *applyPatchPreparedTransaction,
) error {
	for _, intent := range transaction.intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
		if err != nil {
			return err
		}
		rootName := ""
		switch {
		case transaction.effects.forestRollbackQuarantined[intent.id]:
			rootName = intent.rollbackRoot
		case transaction.effects.forestPublished[intent.id]:
			rootName = intent.publicRoot
		default:
			present, presentErr := applyPatchTxnRecoveryIdentityPresent(
				intent.anchor,
				intent.stageRoot,
				func() applyPatchTxnIdentity {
					if forest.StageRoot.Identity == nil {
						return applyPatchTxnIdentity{}
					}
					return *forest.StageRoot.Identity
				}(),
			)
			if presentErr == nil && present {
				rootName = intent.stageRoot
			}
		}
		if rootName == "" {
			continue
		}
		rootPath := filepath.Join(intent.anchorPath, rootName)
		for entryIndex := len(forest.Entries) - 1; entryIndex >= 1; entryIndex-- {
			entry := &forest.Entries[entryIndex]
			if entry.Identity == nil {
				continue
			}
			parentPath := rootPath
			parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
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
			removalIdentity, _, inspectErr := applyPatchTxnIdentityAt(
				parent,
				entry.RemovalBasename,
			)
			if errors.Is(inspectErr, os.ErrNotExist) {
				if entry.RemovalAttempted {
					if syncErr := applyPatchTxnSyncDirectory(parent); syncErr != nil {
						_ = parent.Close()
						return syncErr
					}
					entry.RemovalAttempted = false
					checkpointErr := transaction.checkpoint()
					_ = parent.Close()
					if checkpointErr != nil {
						return checkpointErr
					}
					continue
				}
				_ = parent.Close()
				continue
			}
			if !entry.RemovalAttempted {
				_ = parent.Close()
				return errors.New("apply-patch unexpected forest removal quarantine")
			}
			if inspectErr != nil || !removalIdentity.equal(*entry.Identity) {
				_ = parent.Close()
				return errors.Join(
					errors.New("apply-patch forest removal quarantine conflict"),
					inspectErr,
				)
			}
			basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
			removeErr := applyPatchTxnRemoveExact(
				parent,
				basename,
				entry.RemovalBasename,
				*entry.Identity,
				entry.Kind == "directory",
			)
			if removeErr == nil {
				removeErr = applyPatchTxnSyncDirectory(parent)
			}
			closeErr := parent.Close()
			if removeErr != nil || closeErr != nil {
				return errors.Join(removeErr, closeErr)
			}
			entry.RemovalAttempted = false
			if checkpointErr := transaction.checkpoint(); checkpointErr != nil {
				return checkpointErr
			}
		}
	}
	return nil
}

func validateApplyPatchTxnRecoveryParticipants(
	transaction *applyPatchPreparedTransaction,
) error {
	allowCommittedCleanup := transaction.journal.Phase == applyPatchTransactionPhaseCommitted &&
		transaction.store.committedCleanupAuthenticated
	for _, operation := range transaction.intent.operations {
		if operation.source != nil {
			if err := validateApplyPatchTxnRecoverySourceParticipants(
				transaction,
				operation,
				allowCommittedCleanup,
			); err != nil {
				return err
			}
		}
		if operation.targetAnchor != nil && operation.forest == nil {
			if err := validateApplyPatchTxnRecoveryTargetParticipants(
				transaction,
				operation,
				allowCommittedCleanup,
			); err != nil {
				return err
			}
		}
	}
	for _, forestIntent := range transaction.intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(
			transaction.journal,
			forestIntent.id,
		)
		if err != nil || forest.StageRoot.Identity == nil {
			if transaction.journal.Phase == applyPatchTransactionPhasePreparing {
				continue
			}
			return errors.Join(
				errors.New("apply-patch recovery forest is not checkpointed"),
				err,
			)
		}
		if transaction.journal.Phase == applyPatchTransactionPhasePreparing {
			continue
		}
		switch {
		case transaction.effects.forestPublished[forestIntent.id]:
			if err := verifyApplyPatchTxnPublishedForest(
				forestIntent,
				forest,
				allowCommittedCleanup,
			); err != nil {
				return err
			}
		case transaction.effects.forestRollbackQuarantined[forestIntent.id]:
			if err := verifyApplyPatchTxnRollingForestTreeAt(
				forestIntent,
				forest,
				forestIntent.rollbackRoot,
			); err != nil {
				return err
			}
		default:
			stagePresent, err := applyPatchTxnRecoveryIdentityPresent(
				forestIntent.anchor,
				forestIntent.stageRoot,
				*forest.StageRoot.Identity,
			)
			if err != nil {
				return err
			}
			if stagePresent {
				if err := verifyApplyPatchTxnStagedForest(
					forestIntent,
					forest,
				); err != nil {
					return err
				}
			} else if transaction.journal.Phase != applyPatchTransactionPhaseRollingBack {
				return errors.New("apply-patch recovery staged forest is missing")
			}
		}
	}
	return nil
}

func validateApplyPatchTxnRecoverySourceParticipants(
	transaction *applyPatchPreparedTransaction,
	operation *applyPatchTxnIntent,
	allowCommittedCleanup bool,
) error {
	if err := validateApplyPatchTxnInactiveSourceProbeNames(
		operation,
		transaction.journal,
	); err != nil {
		return err
	}
	before := transaction.journal.Operations[operation.index].Before
	witness, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if err != nil {
		return err
	}
	witnessPresent := false
	if witness.Rooted.Identity != nil {
		witnessPresent, err = applyPatchTxnRecoveryIdentityPresent(
			operation.source.anchor,
			witness.Rooted.Basename,
			*witness.Rooted.Identity,
		)
		if err != nil {
			return err
		}
		if witnessPresent {
			expectedWitnessLinks := witness.Rooted.Links
			if transaction.effects.sourceRestoreRequired[operation.index] &&
				!transaction.effects.sourceQuarantined[operation.index] ||
				allowCommittedCleanup &&
					!transaction.effects.sourceQuarantined[operation.index] {
				expectedWitnessLinks = 1
			}
			state, verifyErr := verifyApplyPatchTxnRegular(
				operation.source.anchor,
				witness.Rooted.Basename,
				before,
				expectedWitnessLinks,
			)
			if verifyErr != nil || !state.Identity.equal(*witness.Rooted.Identity) {
				return errors.Join(
					errors.New("apply-patch recovery source witness changed"),
					verifyErr,
				)
			}
		}
	}
	if transaction.effects.sourceQuarantined[operation.index] {
		quarantine, artifactErr := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if artifactErr != nil || quarantine.Rooted.Identity == nil || !witnessPresent {
			return errors.Join(
				errors.New("apply-patch recovery original ownership witness is missing"),
				artifactErr,
			)
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			quarantine.Rooted.Basename,
			before,
			quarantine.Rooted.Links,
		)
		if verifyErr != nil || !state.Identity.equal(*witness.Rooted.Identity) ||
			!state.Identity.equal(*quarantine.Rooted.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery original quarantine changed"),
				verifyErr,
			)
		}
		return nil
	}
	publicIdentity, _, inspectErr := applyPatchTxnIdentityAt(
		operation.source.anchor,
		operation.source.basename,
	)
	if inspectErr == nil && publicIdentity.equal(operation.source.state.Identity) {
		expectedLinks := uint64(1)
		if witnessPresent {
			expectedLinks = 2
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			operation.source.basename,
			before,
			expectedLinks,
		)
		if verifyErr != nil || !state.Identity.equal(operation.source.state.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery original source changed"),
				verifyErr,
			)
		}
		return nil
	}
	restore, restoreErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceRestoreStage,
	)
	if restoreErr != nil {
		return restoreErr
	}
	if inspectErr == nil && restore.Rooted.Identity != nil &&
		publicIdentity.equal(*restore.Rooted.Identity) {
		stagePresent, stageErr := applyPatchTxnRecoveryIdentityPresent(
			operation.source.anchor,
			restore.Rooted.Basename,
			*restore.Rooted.Identity,
		)
		if stageErr != nil {
			return stageErr
		}
		expectedLinks := uint64(1)
		if stagePresent {
			expectedLinks = 2
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			operation.source.anchor,
			operation.source.basename,
			before,
			expectedLinks,
		)
		if verifyErr != nil || !state.Identity.equal(*restore.Rooted.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery backup-restored source changed"),
				verifyErr,
			)
		}
		return nil
	}
	if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
		return inspectErr
	}
	if transaction.journal.Phase == applyPatchTransactionPhaseCommitted &&
		allowCommittedCleanup {
		return nil
	}
	if transaction.effects.sourceRestoreRequired[operation.index] &&
		!witnessPresent &&
		!(inspectErr == nil && restore.Rooted.Identity != nil &&
			publicIdentity.equal(*restore.Rooted.Identity)) {
		return errors.New("apply-patch recovery backup fallback witness is missing")
	}
	if operation.planned.kind == "update" {
		return nil
	}
	if transaction.effects.sourceRestoreRequired[operation.index] && witnessPresent {
		return nil
	}
	if transaction.journal.Phase == applyPatchTransactionPhaseRollingBack &&
		!witnessPresent {
		return nil
	}
	return errors.New("apply-patch recovery original source is missing")
}

func validateApplyPatchTxnRecoveryTargetParticipants(
	transaction *applyPatchPreparedTransaction,
	operation *applyPatchTxnIntent,
	allowCommittedCleanup bool,
) error {
	after := transaction.journal.Operations[operation.index].After
	stage, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.Identity == nil {
		if transaction.journal.Phase == applyPatchTransactionPhasePreparing {
			return nil
		}
		return errors.Join(errors.New("apply-patch recovery postimage is not checkpointed"), err)
	}
	witness, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if err != nil {
		return err
	}
	witnessPresent := false
	if witness.Rooted.Identity != nil {
		witnessPresent, err = applyPatchTxnRecoveryIdentityPresent(
			operation.targetAnchor,
			witness.Rooted.Basename,
			*witness.Rooted.Identity,
		)
		if err != nil {
			return err
		}
	} else if transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		return errors.New("apply-patch recovery postimage witness is unavailable")
	}
	if witnessPresent {
		expectedWitnessLinks := witness.Rooted.Links
		if transaction.journal.Phase == applyPatchTransactionPhaseRollingBack &&
			!transaction.effects.targetPublished[operation.index] &&
			!transaction.effects.targetRollbackQuarantined[operation.index] {
			stagePresent, stageErr := applyPatchTxnRecoveryIdentityPresent(
				operation.targetAnchor,
				stage.Rooted.Basename,
				*stage.Rooted.Identity,
			)
			if stageErr != nil {
				return stageErr
			}
			if !stagePresent {
				expectedWitnessLinks = 1
			}
		}
		state, verifyErr := verifyApplyPatchTxnRegular(
			operation.targetAnchor,
			witness.Rooted.Basename,
			after,
			expectedWitnessLinks,
		)
		if verifyErr != nil || !state.Identity.equal(*stage.Rooted.Identity) {
			return errors.Join(
				errors.New("apply-patch recovery postimage witness changed"),
				verifyErr,
			)
		}
	}
	var name string
	switch {
	case transaction.effects.targetPublished[operation.index]:
		name = operation.targetLayout.components[0]
	case transaction.effects.targetRollbackQuarantined[operation.index]:
		rollback, artifactErr := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactTargetRollbackQuarantine,
		)
		if artifactErr != nil {
			return artifactErr
		}
		name = rollback.Rooted.Basename
	default:
		present, presentErr := applyPatchTxnRecoveryIdentityPresent(
			operation.targetAnchor,
			stage.Rooted.Basename,
			*stage.Rooted.Identity,
		)
		if presentErr != nil {
			return presentErr
		}
		if present {
			name = stage.Rooted.Basename
		}
	}
	if name == "" {
		if transaction.journal.Phase == applyPatchTransactionPhaseRollingBack ||
			transaction.journal.Phase == applyPatchTransactionPhasePreparing &&
				!witnessPresent {
			return nil
		}
		return errors.New("apply-patch recovery postimage participant is missing")
	}
	if !witnessPresent && !allowCommittedCleanup &&
		transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		return errors.New("apply-patch recovery live postimage witness is missing")
	}
	expectedLinks := uint64(1)
	if witnessPresent {
		expectedLinks = 2
	}
	if transaction.journal.Phase == applyPatchTransactionPhasePreparing &&
		!witnessPresent && stage.Rooted.Links == 1 &&
		name == stage.Rooted.Basename {
		state, inspectErr := applyPatchTxnInspectAt(operation.targetAnchor, name)
		if inspectErr != nil || !state.Identity.equal(*stage.Rooted.Identity) ||
			state.Links != 1 {
			return errors.Join(
				errors.New("apply-patch recovery partial postimage ownership conflict"),
				inspectErr,
			)
		}
		return nil
	}
	state, verifyErr := verifyApplyPatchTxnRegular(
		operation.targetAnchor,
		name,
		after,
		expectedLinks,
	)
	if verifyErr != nil || !state.Identity.equal(*stage.Rooted.Identity) {
		return errors.Join(
			errors.New("apply-patch recovery postimage participant changed"),
			verifyErr,
		)
	}
	return nil
}

func openApplyPatchTxnRecoveryStore(
	workspace *applyPatchTransactionWorkspaceState,
	key []byte,
) (*applyPatchTxnStore, *applyPatchTransactionJournal, error) {
	if workspace == nil {
		return nil, nil, errors.New("apply-patch transaction workspace state is unavailable")
	}
	var activeName string
	var activeInfo os.FileInfo
	var activeRoot *os.Root
	var pointerData []byte
	var pointerInfo os.FileInfo
	var pointerStageData []byte
	var pointerStageInfo os.FileInfo
	err := workspace.withDirectoryAnchor(func(root *os.Root) error {
		entries, err := readApplyPatchTxnRootEntries(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			switch entry.Name() {
			case applyPatchTransactionWorkspaceBindingFile,
				applyPatchTransactionWorkspaceLockFile:
				continue
			case applyPatchTransactionPointerFile:
				if pointerData != nil {
					return errors.New("apply-patch transaction pointer is duplicated")
				}
				var readErr error
				pointerData, pointerInfo, readErr = readApplyPatchTransactionPrivateRegularBounded(
					root,
					applyPatchTransactionPointerFile,
					applyPatchTransactionPointerMaxBytes,
				)
				if readErr != nil {
					return readErr
				}
				continue
			case applyPatchTransactionPointerStageFile:
				var readErr error
				pointerStageData, pointerStageInfo, readErr = readApplyPatchTransactionPrivateRegularBounded(
					root,
					applyPatchTransactionPointerStageFile,
					applyPatchTransactionPointerMaxBytes,
				)
				if readErr != nil {
					return readErr
				}
				continue
			}
			if !strings.HasPrefix(entry.Name(), applyPatchTransactionActiveNamePrefix) &&
				!strings.HasPrefix(entry.Name(), applyPatchTransactionCommitNamePrefix) {
				return errors.New("apply-patch transaction workspace state contains an alien entry")
			}
			if activeName != "" {
				return errors.New("apply-patch transaction workspace state has multiple active entries")
			}
			info, err := root.Lstat(entry.Name())
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.Join(
					errors.New("apply-patch transaction active state is invalid"),
					err,
				)
			}
			validationErr := validateApplyPatchTransactionPrivateObject(info, true)
			if validationErr != nil {
				return validationErr
			}
			opened, err := root.OpenRoot(entry.Name())
			if err != nil {
				return err
			}
			activeName = entry.Name()
			activeInfo = info
			activeRoot = opened
		}
		return nil
	})
	if err != nil {
		if activeRoot != nil {
			_ = activeRoot.Close()
		}
		return nil, nil, err
	}
	pointerData, pointerInfo, err = resolveApplyPatchTxnRecoveryPointerStage(
		workspace,
		key,
		pointerData,
		pointerInfo,
		pointerStageData,
		pointerStageInfo,
	)
	if err != nil {
		if activeRoot != nil {
			_ = activeRoot.Close()
		}
		return nil, nil, err
	}
	var pointer *applyPatchTransactionPointer
	if pointerData != nil {
		pointer, err = decodeApplyPatchTransactionPointer(key, pointerData)
		if err != nil ||
			pointer.Workspace.CanonicalPath != workspace.canonicalWorkspace {
			if activeRoot != nil {
				_ = activeRoot.Close()
			}
			return nil, nil, errors.Join(
				errors.New("apply-patch committed pointer conflict"),
				err,
			)
		}
	}
	if activeName == "" {
		if pointer == nil {
			return nil, nil, os.ErrNotExist
		}
		removeErr := removeApplyPatchTxnRecoveryPointer(
			workspace,
			pointerInfo,
		)
		if removeErr != nil {
			return nil, nil, removeErr
		}
		return nil, nil, os.ErrNotExist
	}
	if !validApplyPatchTransactionHex(
		strings.TrimPrefix(activeName, applyPatchTransactionActiveNamePrefix),
		applyPatchTransactionIDHexBytes,
	) && !validApplyPatchTransactionHex(
		strings.TrimPrefix(activeName, applyPatchTransactionCommitNamePrefix),
		applyPatchTransactionIDHexBytes,
	) {
		_ = activeRoot.Close()
		return nil, nil, errors.New("apply-patch transaction active directory name is invalid")
	}
	committedDirectory := strings.HasPrefix(
		activeName,
		applyPatchTransactionCommitNamePrefix,
	)
	if committedDirectory && pointer == nil {
		_ = activeRoot.Close()
		return nil, nil, errors.New("apply-patch committed directory has no pointer")
	}
	journalData, journalInfo, err := readApplyPatchTransactionPrivateRegularBounded(
		activeRoot,
		applyPatchTransactionJournalFile,
		applyPatchTransactionJournalMaxBytes,
	)
	if err != nil {
		if !committedDirectory && pointer == nil && errors.Is(err, os.ErrNotExist) {
			entries, entriesErr := readApplyPatchTxnRootEntries(activeRoot)
			if entriesErr != nil {
				_ = activeRoot.Close()
				return nil, nil, entriesErr
			}
			switch len(entries) {
			case 0:
				if closeErr := activeRoot.Close(); closeErr != nil {
					return nil, nil, closeErr
				}
				if cleanupErr := cleanupApplyPatchTxnJournalLessActiveDirectory(
					workspace,
					activeName,
					activeInfo,
				); cleanupErr != nil {
					return nil, nil, cleanupErr
				}
				return nil, nil, os.ErrNotExist
			case 1:
				if entries[0].Name() != applyPatchTransactionJournalStageFile {
					_ = activeRoot.Close()
					return nil, nil, errors.New(
						"apply-patch journal-less active directory contains an alien entry",
					)
				}
				var promoteErr error
				journalData, journalInfo, promoteErr = promoteApplyPatchTxnInitialJournalStage(
					activeRoot,
					key,
					workspace,
					activeName,
				)
				if promoteErr != nil {
					_ = activeRoot.Close()
					return nil, nil, promoteErr
				}
				err = nil
			default:
				_ = activeRoot.Close()
				return nil, nil, errors.New(
					"apply-patch journal-less active directory contains alien entries",
				)
			}
		}
	}
	if err != nil {
		if committedDirectory && pointer != nil && errors.Is(err, os.ErrNotExist) {
			entries, entriesErr := readApplyPatchTxnRootEntries(activeRoot)
			if entriesErr != nil || len(entries) != 0 {
				_ = activeRoot.Close()
				return nil, nil, errors.Join(
					errors.New("apply-patch committed cleanup directory is not empty"),
					entriesErr,
				)
			}
			if closeErr := activeRoot.Close(); closeErr != nil {
				return nil, nil, closeErr
			}
			if cleanupErr := removeApplyPatchTxnRecoveryCommittedShell(
				workspace,
				activeName,
				activeInfo,
				pointerInfo,
			); cleanupErr != nil {
				return nil, nil, cleanupErr
			}
			return nil, nil, os.ErrNotExist
		}
		_ = activeRoot.Close()
		return nil, nil, fmt.Errorf("read apply-patch recovery journal: %w", err)
	}
	journal, err := decodeApplyPatchTransactionJournal(key, journalData)
	if err != nil {
		_ = activeRoot.Close()
		return nil, nil, err
	}
	expectedActive := applyPatchTransactionActiveNamePrefix + journal.TransactionID
	expectedCommitted := applyPatchTransactionCommitNamePrefix + journal.TransactionID
	if journal.State.ActiveDirectory != expectedActive ||
		journal.State.CommittedDirectory != expectedCommitted ||
		activeName != expectedActive && activeName != expectedCommitted {
		_ = activeRoot.Close()
		return nil, nil, errors.New("apply-patch transaction active binding conflict")
	}
	journalIdentity, err := applyPatchTxnIdentityFromFileInfo(journalInfo, "regular")
	if err != nil {
		_ = activeRoot.Close()
		return nil, nil, err
	}
	store := &applyPatchTxnStore{
		workspace: workspace, activeName: activeName, activeRoot: activeRoot,
		activeInfo: activeInfo,
		owned: map[string]applyPatchTxnIdentity{
			applyPatchTransactionJournalFile: journalIdentity,
		},
		journalBytes:                  append([]byte(nil), journalData...),
		pointerInfo:                   pointerInfo,
		committedCleanupAuthenticated: pointer != nil,
	}
	if pointer != nil {
		expectedPointer, pointerErr := buildApplyPatchTxnPointer(key, journal)
		if pointerErr != nil || *pointer != *expectedPointer {
			_ = store.Close()
			return nil, nil, errors.Join(
				errors.New("apply-patch committed pointer journal conflict"),
				pointerErr,
			)
		}
	}
	if err := validateApplyPatchTxnRecoveryStateEntries(store, key, journal); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, journal, nil
}

func promoteApplyPatchTxnInitialJournalStage(
	activeRoot *os.Root,
	key []byte,
	workspace *applyPatchTransactionWorkspaceState,
	activeName string,
) ([]byte, os.FileInfo, error) {
	data, stageInfo, err := readApplyPatchTransactionPrivateRegularBounded(
		activeRoot,
		applyPatchTransactionJournalStageFile,
		applyPatchTransactionJournalMaxBytes,
	)
	if err != nil {
		return nil, nil, err
	}
	journal, err := decodeApplyPatchTransactionJournal(key, data)
	if err != nil || journal.Phase != applyPatchTransactionPhasePreparing ||
		journal.DecisionAttempted ||
		journal.Workspace.CanonicalPath != workspace.canonicalWorkspace ||
		journal.State.ActiveDirectory != activeName ||
		activeName != applyPatchTransactionActiveNamePrefix+journal.TransactionID {
		return nil, nil, errors.Join(
			errors.New("apply-patch initial journal stage authentication conflict"),
			err,
		)
	}
	linkErr := activeRoot.Link(
		applyPatchTransactionJournalStageFile,
		applyPatchTransactionJournalFile,
	)
	if linkErr != nil {
		return nil, nil, linkErr
	}
	syncErr := syncApplyPatchTxnRootDirectory(activeRoot)
	if syncErr != nil {
		return nil, nil, syncErr
	}
	_, journalInfo, err := readApplyPatchTransactionPrivateRegularBounded(
		activeRoot,
		applyPatchTransactionJournalFile,
		applyPatchTransactionJournalMaxBytes,
	)
	if err != nil || !os.SameFile(stageInfo, journalInfo) {
		return nil, nil, errors.Join(
			errors.New("apply-patch initial journal changed during publication"),
			err,
		)
	}
	removeErr := removeApplyPatchTransactionExactRootEntry(
		activeRoot,
		applyPatchTransactionJournalStageFile,
		stageInfo,
	)
	if removeErr != nil {
		return nil, nil, removeErr
	}
	syncErr = syncApplyPatchTxnRootDirectory(activeRoot)
	if syncErr != nil {
		return nil, nil, syncErr
	}
	return data, journalInfo, nil
}

func resolveApplyPatchTxnRecoveryPointerStage(
	workspace *applyPatchTransactionWorkspaceState,
	key []byte,
	pointerData []byte,
	pointerInfo os.FileInfo,
	stageData []byte,
	stageInfo os.FileInfo,
) ([]byte, os.FileInfo, error) {
	if stageData == nil {
		return pointerData, pointerInfo, nil
	}
	staged, err := decodeApplyPatchTransactionPointer(key, stageData)
	if err != nil || staged.Workspace.CanonicalPath != workspace.canonicalWorkspace {
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction pointer stage authentication conflict"),
			err,
		)
	}
	if pointerData != nil {
		current, decodeErr := decodeApplyPatchTransactionPointer(key, pointerData)
		if decodeErr != nil || *current != *staged {
			return nil, nil, errors.Join(
				errors.New("apply-patch transaction pointer stage conflicts with pointer"),
				decodeErr,
			)
		}
	}
	err = workspace.withDirectoryAnchor(func(root *os.Root) error {
		if pointerData == nil {
			if linkErr := root.Link(
				applyPatchTransactionPointerStageFile,
				applyPatchTransactionPointerFile,
			); linkErr != nil {
				return linkErr
			}
			if syncErr := syncApplyPatchTxnRootDirectory(root); syncErr != nil {
				return syncErr
			}
			var readErr error
			pointerData, pointerInfo, readErr = readApplyPatchTransactionPrivateRegularBounded(
				root,
				applyPatchTransactionPointerFile,
				applyPatchTransactionPointerMaxBytes,
			)
			if readErr != nil {
				return readErr
			}
		}
		if removeErr := removeApplyPatchTransactionExactRootEntry(
			root,
			applyPatchTransactionPointerStageFile,
			stageInfo,
		); removeErr != nil {
			return removeErr
		}
		return syncApplyPatchTxnRootDirectory(root)
	})
	if err != nil {
		return nil, nil, err
	}
	return pointerData, pointerInfo, nil
}

func buildApplyPatchTxnPointer(
	key []byte,
	journal *applyPatchTransactionJournal,
) (*applyPatchTransactionPointer, error) {
	digest, err := applyPatchTxnCleanupJournalDigest(key, journal)
	if err != nil {
		return nil, err
	}
	return &applyPatchTransactionPointer{
		Version:           applyPatchTransactionPointerVersion,
		Workspace:         journal.Workspace,
		State:             journal.State,
		TransactionID:     journal.TransactionID,
		Phase:             journal.Phase,
		DecisionAttempted: journal.DecisionAttempted,
		JournalSHA256:     digest,
	}, nil
}

func removeApplyPatchTxnRecoveryPointer(
	workspace *applyPatchTransactionWorkspaceState,
	pointerInfo os.FileInfo,
) error {
	if pointerInfo == nil {
		return errors.New("apply-patch committed pointer identity is unavailable")
	}
	return workspace.withDirectoryAnchor(func(root *os.Root) error {
		if err := removeApplyPatchTransactionExactRootEntry(
			root,
			applyPatchTransactionPointerFile,
			pointerInfo,
		); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(root)
	})
}

func removeApplyPatchTxnRecoveryCommittedShell(
	workspace *applyPatchTransactionWorkspaceState,
	directoryName string,
	directoryInfo os.FileInfo,
	pointerInfo os.FileInfo,
) error {
	if directoryInfo == nil || pointerInfo == nil {
		return errors.New("apply-patch committed cleanup identity is unavailable")
	}
	return workspace.withDirectoryAnchor(func(root *os.Root) error {
		current, err := root.Lstat(directoryName)
		if err != nil || !current.IsDir() || !os.SameFile(current, directoryInfo) {
			return errors.Join(
				errors.New("apply-patch committed cleanup directory changed"),
				err,
			)
		}
		if err := root.Remove(directoryName); err != nil {
			return err
		}
		if err := syncApplyPatchTxnRootDirectory(root); err != nil {
			return err
		}
		if err := removeApplyPatchTransactionExactRootEntry(
			root,
			applyPatchTransactionPointerFile,
			pointerInfo,
		); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(root)
	})
}

func validateApplyPatchTxnRecoveryStateEntries(
	store *applyPatchTxnStore,
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	entries, err := readApplyPatchTxnRootEntries(store.activeRoot)
	if err != nil {
		return err
	}
	allowed := map[string]struct{}{applyPatchTransactionJournalFile: {}}
	backupByName := make(map[string]*applyPatchTransactionJournalArtifact)
	foundBackups := make(map[string]bool)
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Role == applyPatchTransactionArtifactBackupBlob {
			allowed[artifact.StateName] = struct{}{}
			backupByName[artifact.StateName] = artifact
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == applyPatchTransactionJournalStageFile {
			data, info, err := readApplyPatchTransactionPrivateRegularBounded(
				store.activeRoot,
				name,
				applyPatchTransactionJournalMaxBytes,
			)
			if err != nil {
				return errors.New("apply-patch transaction journal stage is invalid")
			}
			staged, err := decodeApplyPatchTransactionJournal(key, data)
			if err != nil || !sameApplyPatchTxnJournalTopology(journal, staged) {
				return errors.New("apply-patch transaction journal stage authentication conflict")
			}
			identity, err := applyPatchTxnIdentityFromFileInfo(info, "regular")
			if err != nil {
				return err
			}
			store.journalStageIdentity = copyApplyPatchTxnIdentity(identity)
			continue
		}
		if _, ok := allowed[name]; !ok {
			return errors.New("apply-patch transaction active state contains an alien entry")
		}
		if name == applyPatchTransactionJournalFile {
			continue
		}
		artifact := backupByName[name]
		if artifact == nil || artifact.StateIdentity == nil ||
			artifact.StateLinks != 1 || artifact.Backup == nil {
			return errors.New(
				"apply-patch transaction backup was created before its identity checkpoint",
			)
		}
		data, info, err := readApplyPatchTransactionPrivateRegularBounded(
			store.activeRoot,
			name,
			applyPatchTransactionMaxBackupBytes,
		)
		if err != nil {
			return err
		}
		identity, err := applyPatchTxnIdentityFromFileInfo(info, "regular")
		if err != nil || !identity.equal(*artifact.StateIdentity) {
			return errors.Join(
				errors.New("apply-patch transaction backup identity conflict"),
				err,
			)
		}
		state, inspectErr := store.inspectActiveRegular(name)
		if inspectErr != nil || !state.Identity.equal(identity) || state.Links != 1 {
			return errors.Join(
				errors.New("apply-patch transaction backup link state conflict"),
				inspectErr,
			)
		}
		if journal.Phase != applyPatchTransactionPhasePreparing {
			if err := verifyApplyPatchTransactionBackup(
				key,
				journal.TransactionID,
				name,
				*artifact.Backup,
				data,
			); err != nil {
				return err
			}
		}
		store.owned[name] = identity
		foundBackups[name] = true
	}
	for name, artifact := range backupByName {
		if artifact.StateIdentity != nil && !foundBackups[name] &&
			!store.committedCleanupAuthenticated {
			return errors.New("apply-patch transaction checkpointed backup is missing")
		}
	}
	return nil
}

func (store *applyPatchTxnStore) finishRecoveryJournalStage() error {
	if store == nil || store.journalStageIdentity == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.revalidateLocked(); err != nil {
		return err
	}
	if err := removeApplyPatchTxnRootIdentity(
		store.activeRoot,
		applyPatchTransactionJournalStageFile,
		*store.journalStageIdentity,
	); err != nil {
		return err
	}
	if err := syncApplyPatchTxnRootDirectory(store.activeRoot); err != nil {
		return err
	}
	store.journalStageIdentity = nil
	return nil
}

func sameApplyPatchTxnJournalTopology(
	left *applyPatchTransactionJournal,
	right *applyPatchTransactionJournal,
) bool {
	if left == nil || right == nil || left.Version != right.Version ||
		left.Workspace != right.Workspace || left.State != right.State ||
		left.TransactionID != right.TransactionID ||
		left.OperationCount != right.OperationCount ||
		len(left.Operations) != len(right.Operations) ||
		len(left.Artifacts) != len(right.Artifacts) ||
		len(left.Forests) != len(right.Forests) {
		return false
	}
	for index := range left.Operations {
		leftOp := &left.Operations[index]
		rightOp := &right.Operations[index]
		if leftOp.Index != rightOp.Index || leftOp.Kind != rightOp.Kind ||
			leftOp.Before != rightOp.Before || leftOp.After != rightOp.After ||
			leftOp.ForestID != rightOp.ForestID ||
			!sameApplyPatchTxnJournalEndpoint(leftOp.Source, rightOp.Source) ||
			!sameApplyPatchTxnJournalEndpoint(leftOp.Target, rightOp.Target) {
			return false
		}
	}
	for index := range left.Artifacts {
		leftArtifact := &left.Artifacts[index]
		rightArtifact := &right.Artifacts[index]
		if leftArtifact.OperationIndex != rightArtifact.OperationIndex ||
			leftArtifact.Role != rightArtifact.Role ||
			leftArtifact.StateName != rightArtifact.StateName ||
			leftArtifact.Expected != rightArtifact.Expected ||
			!sameApplyPatchTxnJournalRootedTopology(
				leftArtifact.Rooted,
				rightArtifact.Rooted,
			) || !sameApplyPatchTxnBackupRecord(leftArtifact.Backup, rightArtifact.Backup) {
			return false
		}
	}
	for index := range left.Forests {
		leftForest := &left.Forests[index]
		rightForest := &right.Forests[index]
		if leftForest.ID != rightForest.ID ||
			!slices.Equal(leftForest.OperationIndexes, rightForest.OperationIndexes) ||
			leftForest.PublicRoot != rightForest.PublicRoot ||
			leftForest.SentinelRelativePath != rightForest.SentinelRelativePath ||
			!sameApplyPatchTxnJournalRootedTopology(
				&leftForest.StageRoot,
				&rightForest.StageRoot,
			) || !sameApplyPatchTxnJournalRootedTopology(
			&leftForest.RollbackRoot,
			&rightForest.RollbackRoot,
		) || !sameApplyPatchTxnJournalRootedTopology(
			&leftForest.SentinelWitness,
			&rightForest.SentinelWitness,
		) || len(leftForest.Entries) != len(rightForest.Entries) {
			return false
		}
		for entryIndex := range leftForest.Entries {
			leftEntry := &leftForest.Entries[entryIndex]
			rightEntry := &rightForest.Entries[entryIndex]
			if leftEntry.RelativePath != rightEntry.RelativePath ||
				leftEntry.CanonicalPath != rightEntry.CanonicalPath ||
				leftEntry.Kind != rightEntry.Kind || leftEntry.Mode != rightEntry.Mode ||
				leftEntry.Length != rightEntry.Length || leftEntry.SHA256 != rightEntry.SHA256 ||
				leftEntry.RemovalBasename != rightEntry.RemovalBasename ||
				!sameApplyPatchTxnOptionalInt(
					leftEntry.OperationIndex,
					rightEntry.OperationIndex,
				) {
				return false
			}
		}
	}
	return true
}

func sameApplyPatchTxnJournalEndpoint(
	left *applyPatchTransactionJournalEndpoint,
	right *applyPatchTransactionJournalEndpoint,
) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Label == right.Label && left.CanonicalPath == right.CanonicalPath &&
		left.PreflightLinks == right.PreflightLinks &&
		sameApplyPatchTxnOptionalIdentity(left.PreflightIdentity, right.PreflightIdentity)
}

func sameApplyPatchTxnJournalRootedTopology(
	left *applyPatchTransactionJournalRootedLocation,
	right *applyPatchTransactionJournalRootedLocation,
) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.AnchorCanonicalPath == right.AnchorCanonicalPath &&
		left.AnchorIdentity.equal(right.AnchorIdentity) && left.Basename == right.Basename &&
		left.RemovalBasename == right.RemovalBasename
}

func sameApplyPatchTxnBackupRecord(
	left *applyPatchTransactionBackupRecord,
	right *applyPatchTransactionBackupRecord,
) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func sameApplyPatchTxnOptionalIdentity(
	left *applyPatchTxnIdentity,
	right *applyPatchTxnIdentity,
) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.equal(*right)
}

func sameApplyPatchTxnOptionalInt(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func validateApplyPatchTxnRecoveryBindings(
	state *applyPatchTransactionState,
	workspaceState *applyPatchTransactionWorkspaceState,
	workspace applyPatchWorkspace,
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(workspace)
	if err != nil || workspaceBinding != journal.Workspace {
		return errors.Join(errors.New("apply-patch recovery workspace binding conflict"), err)
	}
	rootPath, err := state.rootPath()
	if err != nil {
		return err
	}
	rootIdentity, err := state.rootIdentity()
	if err != nil {
		return err
	}
	workspaceRelative, err := workspaceState.directoryRelative()
	if err != nil {
		return err
	}
	expectedState, err := newApplyPatchTxnStateBinding(
		rootPath,
		rootIdentity,
		key,
		workspaceRelative,
		&applyPatchTxnIntentPlan{
			activeName:    journal.State.ActiveDirectory,
			committedName: journal.State.CommittedDirectory,
		},
	)
	if err != nil || expectedState != journal.State {
		return errors.Join(errors.New("apply-patch recovery state binding conflict"), err)
	}
	return nil
}

func (t *ApplyPatchTool) authorizeApplyPatchTxnRecovery(
	ctx context.Context,
	workspace applyPatchWorkspace,
	journal *applyPatchTransactionJournal,
) error {
	protectedRoots, err := snapshotApplyPatchProtectedRoots(workspace, t.protectedRoots)
	if err != nil {
		return err
	}
	plan := &applyPatchPlan{workspace: workspace, protectedRoots: protectedRoots}
	for index := range journal.Operations {
		operation := &journal.Operations[index]
		for roleIndex, endpoint := range []*applyPatchTransactionJournalEndpoint{
			operation.Source,
			operation.Target,
		} {
			if endpoint == nil || roleIndex == 1 && operation.Kind == "update" {
				continue
			}
			if err := t.guardApplyPatchPath(ctx, endpoint.Label, roleIndex == 1); err != nil {
				return fmt.Errorf("apply-patch recovery authorization changed: %w", err)
			}
			if err := t.authorizeApplyPatchCanonical(plan, endpoint.CanonicalPath); err != nil {
				return fmt.Errorf("apply-patch recovery authorization changed: %w", err)
			}
		}
	}
	return nil
}

func reconstructApplyPatchTxnIntent(
	journal *applyPatchTransactionJournal,
) (*applyPatchTxnIntentPlan, error) {
	if journal == nil {
		return nil, errors.New("apply-patch recovery journal is unavailable")
	}
	intent := &applyPatchTxnIntentPlan{
		id:            journal.TransactionID,
		activeName:    journal.State.ActiveDirectory,
		committedName: journal.State.CommittedDirectory,
		operations:    make([]*applyPatchTxnIntent, len(journal.Operations)),
		forests:       make([]*applyPatchTxnForestIntent, 0, len(journal.Forests)),
	}
	failed := true
	defer func() {
		if failed {
			_ = intent.Close()
		}
	}()
	forestByID := make(map[string]*applyPatchTxnForestIntent, len(journal.Forests))
	for index := range journal.Forests {
		forestRecord := &journal.Forests[index]
		forestIntent := &applyPatchTxnForestIntent{
			id:                   forestRecord.ID,
			anchorPath:           forestRecord.StageRoot.AnchorCanonicalPath,
			publicRoot:           filepath.Base(forestRecord.PublicRoot),
			stageRoot:            forestRecord.StageRoot.Basename,
			rollbackRoot:         forestRecord.RollbackRoot.Basename,
			sentinelRelativePath: forestRecord.SentinelRelativePath,
			sentinelWitnessName:  forestRecord.SentinelWitness.Basename,
		}
		anchor, err := openApplyPatchTxnAnchor(forestIntent.anchorPath)
		if err != nil || !anchor.identity.equal(forestRecord.StageRoot.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return nil, errors.Join(
				errors.New("apply-patch recovery forest anchor changed"),
				err,
			)
		}
		forestIntent.anchor = anchor
		intent.forests = append(intent.forests, forestIntent)
		forestByID[forestIntent.id] = forestIntent
	}
	for index := range journal.Operations {
		operationRecord := &journal.Operations[index]
		operation := &applyPatchTxnIntent{
			index: operationRecord.Index,
			planned: plannedApplyPatchOp{
				kind: operationRecord.Kind,
				mode: os.FileMode(operationRecord.Before.Mode),
			},
		}
		if operationRecord.Source != nil {
			operation.planned.sourceLabel = operationRecord.Source.Label
			operation.planned.sourcePath = operationRecord.Source.CanonicalPath
			anchor, err := openApplyPatchTxnAnchor(
				filepath.Dir(operationRecord.Source.CanonicalPath),
			)
			if err != nil {
				return nil, err
			}
			operation.source = &applyPatchTxnEndpoint{
				anchor:   anchor,
				basename: filepath.Base(operationRecord.Source.CanonicalPath),
			}
			if operationRecord.Source.PreflightIdentity != nil {
				operation.source.state.Identity = *operationRecord.Source.PreflightIdentity
				operation.source.state.Links = operationRecord.Source.PreflightLinks
			}
			for _, role := range []applyPatchTransactionArtifactRole{
				applyPatchTransactionArtifactSourceWitness,
				applyPatchTransactionArtifactSourceQuarantine,
				applyPatchTransactionArtifactSourceRestoreStage,
				applyPatchTransactionArtifactBackupBlob,
			} {
				artifact, artifactErr := requireApplyPatchTxnArtifact(journal, index, role)
				if artifactErr != nil {
					return nil, artifactErr
				}
				switch role {
				case applyPatchTransactionArtifactSourceWitness:
					operation.sourceWitnessName = artifact.Rooted.Basename
				case applyPatchTransactionArtifactSourceQuarantine:
					operation.sourceQuarantine = artifact.Rooted.Basename
				case applyPatchTransactionArtifactSourceRestoreStage:
					operation.sourceRestoreStage = artifact.Rooted.Basename
				case applyPatchTransactionArtifactBackupBlob:
					operation.backupName = artifact.StateName
				}
			}
		}
		if operationRecord.Target != nil {
			operation.planned.targetLabel = operationRecord.Target.Label
			operation.planned.targetPath = operationRecord.Target.CanonicalPath
			layout, err := resolveApplyPatchTxnTargetLayoutForRecovery(
				operationRecord.Target.CanonicalPath,
				operationRecord.ForestID,
				journal,
			)
			if err != nil {
				return nil, err
			}
			operation.targetLayout = layout
			if operationRecord.ForestID != "" {
				operation.forest = forestByID[operationRecord.ForestID]
				if operation.forest == nil {
					return nil, errors.New("apply-patch recovery forest binding is missing")
				}
				operation.forest.operations = append(operation.forest.operations, operation)
			} else {
				stage, err := requireApplyPatchTxnArtifact(
					journal,
					index,
					applyPatchTransactionArtifactPostimageStage,
				)
				if err != nil {
					return nil, err
				}
				anchor, err := openApplyPatchTxnAnchor(stage.Rooted.AnchorCanonicalPath)
				if err != nil || !anchor.identity.equal(stage.Rooted.AnchorIdentity) {
					if anchor != nil {
						_ = anchor.Close()
					}
					return nil, errors.Join(
						errors.New("apply-patch recovery target anchor changed"),
						err,
					)
				}
				operation.targetAnchor = anchor
				operation.stageName = stage.Rooted.Basename
				witness, _ := requireApplyPatchTxnArtifact(
					journal, index, applyPatchTransactionArtifactPostimageWitness,
				)
				rollback, _ := requireApplyPatchTxnArtifact(
					journal, index, applyPatchTransactionArtifactTargetRollbackQuarantine,
				)
				operation.postWitnessName = witness.Rooted.Basename
				operation.targetRollback = rollback.Rooted.Basename
			}
		}
		intent.operations[index] = operation
	}
	failed = false
	return intent, nil
}

func resolveApplyPatchTxnTargetLayoutForRecovery(
	target string,
	forestID string,
	journal *applyPatchTransactionJournal,
) (applyPatchTxnTargetLayout, error) {
	if forestID == "" {
		return applyPatchTxnTargetLayout{
			anchorPath: filepath.Dir(target),
			components: []string{filepath.Base(target)},
		}, nil
	}
	forest, err := requireApplyPatchTxnJournalForest(journal, forestID)
	if err != nil {
		return applyPatchTxnTargetLayout{}, err
	}
	relative, err := filepath.Rel(forest.PublicRoot, target)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return applyPatchTxnTargetLayout{}, errors.New(
			"apply-patch recovery forest target binding is invalid",
		)
	}
	components := append(
		[]string{filepath.Base(forest.PublicRoot)},
		strings.Split(filepath.ToSlash(relative), "/")...,
	)
	return applyPatchTxnTargetLayout{
		anchorPath: filepath.Dir(forest.PublicRoot),
		components: components,
	}, nil
}

func classifyApplyPatchTxnRecovery(
	transaction *applyPatchPreparedTransaction,
) error {
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err != nil {
		return err
	}
	for _, operation := range transaction.intent.operations {
		var stage *applyPatchTransactionJournalArtifact
		if operation.targetAnchor != nil && operation.forest == nil {
			var err error
			stage, err = requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactPostimageStage,
			)
			if err != nil {
				return err
			}
		}
		publicSourceState := applyPatchTxnRecoveryAbsent
		if operation.source != nil {
			allowed := map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
				applyPatchTxnRecoveryOriginal: operation.source.state.Identity,
			}
			if operation.planned.kind == "update" && stage != nil &&
				stage.Rooted.Identity != nil {
				allowed[applyPatchTxnRecoveryPostimage] = *stage.Rooted.Identity
			}
			restore, restoreErr := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactSourceRestoreStage,
			)
			if restoreErr != nil {
				return restoreErr
			}
			if restore.Rooted.Identity != nil {
				allowed[applyPatchTxnRecoveryRestored] = *restore.Rooted.Identity
			}
			var sourceErr error
			publicSourceState, sourceErr = inspectApplyPatchTxnRecoveryObject(
				operation.source.anchor,
				operation.source.basename,
				allowed,
			)
			if sourceErr != nil {
				return sourceErr
			}
			quarantine, err := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactSourceQuarantine,
			)
			if err != nil {
				return err
			}
			var quarantineState applyPatchTxnRecoveryObjectState
			if quarantine.Rooted.Identity != nil {
				quarantineState, err = inspectApplyPatchTxnRecoveryObject(
					operation.source.anchor,
					quarantine.Rooted.Basename,
					map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
						applyPatchTxnRecoveryOriginal: *quarantine.Rooted.Identity,
					},
				)
				if err != nil {
					return err
				}
			} else {
				quarantineState, err = inspectApplyPatchTxnUncheckpointedQuarantine(
					operation,
					quarantine,
					transaction.journal,
				)
				if err != nil {
					return err
				}
			}
			quarantinePresent := quarantineState == applyPatchTxnRecoveryOriginal
			if err := validateApplyPatchTxnRecoverySourceState(
				transaction.journal.Phase,
				operation.planned.kind,
				publicSourceState,
				quarantinePresent,
			); err != nil {
				return err
			}
			transaction.effects.sourceQuarantined[operation.index] = quarantinePresent
			transaction.effects.sourceRestoreRequired[operation.index] = !quarantinePresent &&
				(publicSourceState == applyPatchTxnRecoveryAbsent ||
					publicSourceState == applyPatchTxnRecoveryRestored ||
					operation.planned.kind == "update" &&
						publicSourceState == applyPatchTxnRecoveryPostimage)
		}
		if operation.targetAnchor != nil && operation.forest == nil {
			if err := classifyApplyPatchTxnRecoveryTarget(
				transaction,
				operation,
				stage,
				publicSourceState,
			); err != nil {
				return err
			}
		}
	}
	for _, forestIntent := range transaction.intent.forests {
		forest, err := requireApplyPatchTxnJournalForest(
			transaction.journal,
			forestIntent.id,
		)
		if err != nil {
			return err
		}
		if forest.StageRoot.Identity == nil {
			if transaction.journal.Phase != applyPatchTransactionPhasePreparing {
				return errors.New("apply-patch recovery forest is unavailable")
			}
			for _, name := range []string{
				forestIntent.stageRoot,
				forestIntent.publicRoot,
				forestIntent.rollbackRoot,
				forestIntent.sentinelWitnessName,
			} {
				absenceErr := requireApplyPatchTxnAbsent(forestIntent.anchor, name)
				if absenceErr != nil {
					return errors.New(
						"apply-patch recovery uncheckpointed forest cleanup conflict",
					)
				}
			}
			continue
		}
		publicPresent, err := applyPatchTxnRecoveryIdentityPresent(
			forestIntent.anchor,
			forestIntent.publicRoot,
			*forest.StageRoot.Identity,
		)
		if err != nil {
			return err
		}
		stagePresent, err := applyPatchTxnRecoveryIdentityPresent(
			forestIntent.anchor,
			forestIntent.stageRoot,
			*forest.StageRoot.Identity,
		)
		if err != nil {
			return err
		}
		rollbackPresent := false
		if forest.RollbackRoot.Identity != nil {
			rollbackPresent, err = applyPatchTxnRecoveryIdentityPresent(
				forestIntent.anchor,
				forestIntent.rollbackRoot,
				*forest.RollbackRoot.Identity,
			)
			if err != nil {
				return err
			}
		} else {
			identity, _, inspectErr := applyPatchTxnIdentityAt(
				forestIntent.anchor,
				forestIntent.rollbackRoot,
			)
			if inspectErr == nil {
				if transaction.journal.Phase != applyPatchTransactionPhaseRollingBack ||
					!identity.equal(*forest.StageRoot.Identity) {
					return errors.New(
						"apply-patch recovery forest rollback ownership conflict",
					)
				}
				if verifyErr := verifyApplyPatchTxnForestTreeAt(
					forestIntent,
					forest,
					forestIntent.rollbackRoot,
					false,
				); verifyErr != nil {
					return verifyErr
				}
				forest.RollbackRoot.Identity = copyApplyPatchTxnIdentity(identity)
				rollbackPresent = true
			} else if !errors.Is(inspectErr, os.ErrNotExist) {
				return inspectErr
			}
		}
		valid := false
		switch transaction.journal.Phase {
		case applyPatchTransactionPhasePreparing:
			valid = stagePresent && !publicPresent && !rollbackPresent ||
				transaction.store.committedCleanupAuthenticated &&
					!stagePresent && !publicPresent && !rollbackPresent
		case applyPatchTransactionPhasePrepared:
			valid = publicPresent != stagePresent && !rollbackPresent
		case applyPatchTransactionPhaseRollingBack:
			count := 0
			for _, present := range []bool{publicPresent, stagePresent, rollbackPresent} {
				if present {
					count++
				}
			}
			valid = count <= 1
		case applyPatchTransactionPhaseCommitted:
			valid = publicPresent && !stagePresent && !rollbackPresent
		}
		if !valid {
			return errors.New("apply-patch recovery forest state is ambiguous")
		}
		transaction.effects.forestPublished[forestIntent.id] = publicPresent
		transaction.effects.forestRollbackQuarantined[forestIntent.id] = rollbackPresent
	}
	return nil
}

func validateApplyPatchTxnUncheckpointedRootedArtifacts(
	transaction *applyPatchPreparedTransaction,
) error {
	for index := range transaction.journal.Artifacts {
		artifact := &transaction.journal.Artifacts[index]
		if artifact.Rooted == nil || artifact.Rooted.Identity != nil {
			continue
		}
		deferredEffectIdentity := artifact.Role ==
			applyPatchTransactionArtifactSourceQuarantine &&
			(transaction.journal.Phase == applyPatchTransactionPhasePrepared ||
				transaction.journal.Phase == applyPatchTransactionPhaseRollingBack) ||
			artifact.Role == applyPatchTransactionArtifactTargetRollbackQuarantine &&
				transaction.journal.Phase == applyPatchTransactionPhaseRollingBack
		anchor, err := openApplyPatchTxnAnchor(artifact.Rooted.AnchorCanonicalPath)
		if err != nil || !anchor.identity.equal(artifact.Rooted.AnchorIdentity) {
			if anchor != nil {
				_ = anchor.Close()
			}
			return errors.Join(
				errors.New("apply-patch recovery artifact anchor changed"),
				err,
			)
		}
		_, _, inspectErr := applyPatchTxnIdentityAt(anchor, artifact.Rooted.Basename)
		closeErr := anchor.Close()
		if closeErr != nil {
			return closeErr
		}
		if errors.Is(inspectErr, os.ErrNotExist) ||
			inspectErr == nil && deferredEffectIdentity {
			continue
		}
		if inspectErr != nil {
			return inspectErr
		}
		return errors.New(
			"apply-patch transaction private artifact was created before its identity checkpoint",
		)
	}
	for index := range transaction.journal.Forests {
		forest := &transaction.journal.Forests[index]
		for _, location := range []*applyPatchTransactionJournalRootedLocation{
			&forest.StageRoot,
			&forest.SentinelWitness,
		} {
			if location.Identity != nil {
				continue
			}
			anchor, err := openApplyPatchTxnAnchor(location.AnchorCanonicalPath)
			if err != nil || !anchor.identity.equal(location.AnchorIdentity) {
				if anchor != nil {
					_ = anchor.Close()
				}
				return errors.Join(
					errors.New("apply-patch recovery forest anchor changed"),
					err,
				)
			}
			_, _, inspectErr := applyPatchTxnIdentityAt(anchor, location.Basename)
			closeErr := anchor.Close()
			if closeErr != nil {
				return closeErr
			}
			if errors.Is(inspectErr, os.ErrNotExist) {
				continue
			}
			if inspectErr != nil {
				return inspectErr
			}
			return errors.New(
				"apply-patch transaction forest artifact was created before its identity checkpoint",
			)
		}
	}
	return nil
}

type applyPatchTxnRecoveryObjectState string

const (
	applyPatchTxnRecoveryAbsent    applyPatchTxnRecoveryObjectState = "absent"
	applyPatchTxnRecoveryOriginal  applyPatchTxnRecoveryObjectState = "original"
	applyPatchTxnRecoveryPostimage applyPatchTxnRecoveryObjectState = "postimage"
	applyPatchTxnRecoveryRollback  applyPatchTxnRecoveryObjectState = "rollback"
	applyPatchTxnRecoveryRestored  applyPatchTxnRecoveryObjectState = "restored"
)

func inspectApplyPatchTxnRecoveryObject(
	anchor *applyPatchTxnAnchor,
	basename string,
	allowed map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity,
) (applyPatchTxnRecoveryObjectState, error) {
	identity, _, err := applyPatchTxnIdentityAt(anchor, basename)
	if errors.Is(err, os.ErrNotExist) {
		return applyPatchTxnRecoveryAbsent, nil
	}
	if err != nil {
		return "", err
	}
	for state, expected := range allowed {
		if identity.equal(expected) {
			return state, nil
		}
	}
	return "", errors.New("apply-patch recovery object identity conflict")
}

func inspectApplyPatchTxnUncheckpointedQuarantine(
	operation *applyPatchTxnIntent,
	quarantine *applyPatchTransactionJournalArtifact,
	journal *applyPatchTransactionJournal,
) (applyPatchTxnRecoveryObjectState, error) {
	identity, _, err := applyPatchTxnIdentityAt(
		operation.source.anchor,
		quarantine.Rooted.Basename,
	)
	if errors.Is(err, os.ErrNotExist) {
		return applyPatchTxnRecoveryAbsent, nil
	}
	if err != nil {
		return "", err
	}
	witness, err := requireApplyPatchTxnArtifact(
		journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if err != nil || witness.Rooted.Identity == nil ||
		!identity.equal(*witness.Rooted.Identity) {
		return "", errors.Join(
			errors.New("apply-patch recovery uncheckpointed quarantine conflict"),
			err,
		)
	}
	state, err := verifyApplyPatchTxnRegular(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		journal.Operations[operation.index].Before,
		witness.Rooted.Links,
	)
	if err != nil || !state.Identity.equal(*witness.Rooted.Identity) {
		return "", errors.Join(
			errors.New("apply-patch recovery quarantine link conflict"),
			err,
		)
	}
	quarantine.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	quarantine.Rooted.Links = state.Links
	return applyPatchTxnRecoveryOriginal, nil
}

func validateApplyPatchTxnRecoverySourceState(
	phase applyPatchTransactionPhase,
	kind string,
	public applyPatchTxnRecoveryObjectState,
	quarantine bool,
) error {
	valid := false
	switch phase {
	case applyPatchTransactionPhasePreparing:
		valid = public == applyPatchTxnRecoveryOriginal && !quarantine
	case applyPatchTransactionPhasePrepared:
		valid = public == applyPatchTxnRecoveryOriginal && !quarantine ||
			public == applyPatchTxnRecoveryAbsent && quarantine ||
			public == applyPatchTxnRecoveryAbsent && !quarantine ||
			kind == "update" && public == applyPatchTxnRecoveryPostimage
	case applyPatchTransactionPhaseRollingBack:
		valid = public == applyPatchTxnRecoveryOriginal && !quarantine ||
			public == applyPatchTxnRecoveryAbsent && quarantine ||
			public == applyPatchTxnRecoveryAbsent && !quarantine ||
			public == applyPatchTxnRecoveryRestored && !quarantine ||
			kind == "update" && public == applyPatchTxnRecoveryPostimage
	case applyPatchTransactionPhaseCommitted:
		valid = kind == "update" && public == applyPatchTxnRecoveryPostimage ||
			kind != "update" && public == applyPatchTxnRecoveryAbsent
	}
	if !valid {
		return errors.New("apply-patch recovery source state is ambiguous")
	}
	return nil
}

func classifyApplyPatchTxnRecoveryTarget(
	transaction *applyPatchPreparedTransaction,
	operation *applyPatchTxnIntent,
	stage *applyPatchTransactionJournalArtifact,
	publicSourceState applyPatchTxnRecoveryObjectState,
) error {
	if stage == nil || stage.Rooted.Identity == nil {
		if transaction.journal.Phase == applyPatchTransactionPhasePreparing {
			return nil
		}
		return errors.New("apply-patch recovery stage is unavailable")
	}
	var publicState applyPatchTxnRecoveryObjectState
	if operation.planned.kind == "update" {
		publicState = publicSourceState
	} else {
		var err error
		publicState, err = inspectApplyPatchTxnRecoveryObject(
			operation.targetAnchor,
			operation.targetLayout.components[0],
			map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
				applyPatchTxnRecoveryPostimage: *stage.Rooted.Identity,
			},
		)
		if err != nil {
			return err
		}
	}
	stageState, err := inspectApplyPatchTxnRecoveryObject(
		operation.targetAnchor,
		stage.Rooted.Basename,
		map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
			applyPatchTxnRecoveryPostimage: *stage.Rooted.Identity,
		},
	)
	if err != nil {
		return err
	}
	rollback, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactTargetRollbackQuarantine,
	)
	if err != nil {
		return err
	}
	rollbackState := applyPatchTxnRecoveryAbsent
	if rollback.Rooted.Identity != nil {
		rollbackState, err = inspectApplyPatchTxnRecoveryObject(
			operation.targetAnchor,
			rollback.Rooted.Basename,
			map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
				applyPatchTxnRecoveryRollback: *rollback.Rooted.Identity,
			},
		)
		if err != nil {
			return err
		}
	} else {
		identity, _, inspectErr := applyPatchTxnIdentityAt(
			operation.targetAnchor,
			rollback.Rooted.Basename,
		)
		if inspectErr == nil {
			if transaction.journal.Phase != applyPatchTransactionPhaseRollingBack {
				return errors.New(
					"apply-patch recovery rollback artifact exists outside rollback",
				)
			}
			witness, witnessErr := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactPostimageWitness,
			)
			if witnessErr != nil || witness.Rooted.Identity == nil ||
				!identity.equal(*witness.Rooted.Identity) {
				return errors.Join(
					errors.New("apply-patch recovery rollback ownership conflict"),
					witnessErr,
				)
			}
			state, verifyErr := verifyApplyPatchTxnRegular(
				operation.targetAnchor,
				rollback.Rooted.Basename,
				rollback.Expected,
				witness.Rooted.Links,
			)
			if verifyErr != nil || !state.Identity.equal(*witness.Rooted.Identity) {
				return errors.Join(
					errors.New("apply-patch recovery rollback artifact changed"),
					verifyErr,
				)
			}
			rollback.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
			rollback.Rooted.Links = state.Links
			rollbackState = applyPatchTxnRecoveryRollback
		} else if !errors.Is(inspectErr, os.ErrNotExist) {
			return inspectErr
		}
	}
	postPublic := publicState == applyPatchTxnRecoveryPostimage
	stagePresent := stageState == applyPatchTxnRecoveryPostimage
	rollbackPresent := rollbackState == applyPatchTxnRecoveryRollback
	valid := false
	switch transaction.journal.Phase {
	case applyPatchTransactionPhasePreparing:
		valid = !postPublic && !rollbackPresent
	case applyPatchTransactionPhasePrepared:
		valid = !rollbackPresent && postPublic != stagePresent
	case applyPatchTransactionPhaseRollingBack:
		presentCount := 0
		for _, present := range []bool{postPublic, stagePresent, rollbackPresent} {
			if present {
				presentCount++
			}
		}
		valid = presentCount <= 1
	case applyPatchTransactionPhaseCommitted:
		valid = postPublic && !stagePresent && !rollbackPresent
	}
	if !valid {
		return errors.New("apply-patch recovery target state is ambiguous")
	}
	transaction.effects.targetPublished[operation.index] = postPublic
	transaction.effects.targetRollbackQuarantined[operation.index] = rollbackPresent
	return nil
}

func applyPatchTxnRecoveryIdentityPresent(
	anchor *applyPatchTxnAnchor,
	basename string,
	expected applyPatchTxnIdentity,
) (bool, error) {
	identity, _, err := applyPatchTxnIdentityAt(anchor, basename)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !identity.equal(expected) {
		return false, errors.New("apply-patch recovery object identity conflict")
	}
	return true, nil
}
