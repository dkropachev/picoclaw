package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	applyPatchTransactionJournalFile       = "journal.json"
	applyPatchTransactionJournalStageFile  = ".journal.next"
	applyPatchTransactionPointerFile       = "transaction.pointer"
	applyPatchTransactionPointerStageFile  = ".transaction.pointer.stage"
	applyPatchTransactionActiveNamePrefix  = "active-"
	applyPatchTransactionCommitNamePrefix  = "committed-"
	applyPatchTransactionStateFileMaxBytes = applyPatchTransactionJournalMaxBytes
)

type applyPatchTxnStore struct {
	mu sync.Mutex

	workspace                     *applyPatchTransactionWorkspaceState
	activeName                    string
	activeRoot                    *os.Root
	activeInfo                    os.FileInfo
	owned                         map[string]applyPatchTxnIdentity
	journalBytes                  []byte
	journalStageIdentity          *applyPatchTxnIdentity
	pointerInfo                   os.FileInfo
	committedCleanupAuthenticated bool
	closed                        bool
}

func (store *applyPatchTxnStore) prepareCommittedCleanup(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	if journal == nil ||
		journal.Phase != applyPatchTransactionPhaseCommitted ||
		!journal.DecisionAttempted {
		return errors.New("apply-patch committed cleanup state is invalid")
	}
	return store.preparePrivateCleanup(key, journal)
}

func (store *applyPatchTxnStore) preparePrivateCleanup(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	if store == nil || journal == nil {
		return errors.New("apply-patch private cleanup state is invalid")
	}
	journalDigest, err := applyPatchTxnCleanupJournalDigest(key, journal)
	if err != nil {
		return err
	}
	pointer := &applyPatchTransactionPointer{
		Version:           applyPatchTransactionPointerVersion,
		Workspace:         journal.Workspace,
		State:             journal.State,
		TransactionID:     journal.TransactionID,
		Phase:             journal.Phase,
		DecisionAttempted: journal.DecisionAttempted,
		JournalSHA256:     journalDigest,
	}
	pointerBytes, err := encodeApplyPatchTransactionPointer(key, pointer)
	if err != nil {
		return err
	}
	workspacePath, err := store.workspace.directoryPath()
	if err != nil {
		return err
	}
	err = store.workspace.withDirectoryAnchor(func(root *os.Root) error {
		data, info, readErr := readApplyPatchTransactionPrivateRegularBounded(
			root,
			applyPatchTransactionPointerFile,
			applyPatchTransactionPointerMaxBytes,
		)
		if readErr == nil {
			existing, decodeErr := decodeApplyPatchTransactionPointer(key, data)
			if decodeErr != nil || *existing != *pointer {
				return errors.New("apply-patch committed pointer conflict")
			}
			store.pointerInfo = info
			return nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if publishErr := publishApplyPatchTransactionPrivateRegular(
			root,
			workspacePath,
			applyPatchTransactionPointerFile,
			pointerBytes,
		); publishErr != nil {
			return publishErr
		}
		_, info, readErr = readApplyPatchTransactionPrivateRegularBounded(
			root,
			applyPatchTransactionPointerFile,
			applyPatchTransactionPointerMaxBytes,
		)
		if readErr != nil {
			return readErr
		}
		store.pointerInfo = info
		return nil
	})
	if err != nil {
		return err
	}
	store.committedCleanupAuthenticated = true
	return store.quarantineCommittedDirectory()
}

func applyPatchTxnCleanupJournalDigest(
	key []byte,
	journal *applyPatchTransactionJournal,
) (string, error) {
	if journal == nil {
		return "", errors.New("apply-patch cleanup journal is unavailable")
	}
	normalized := *journal
	normalized.Artifacts = make(
		[]applyPatchTransactionJournalArtifact,
		len(journal.Artifacts),
	)
	copy(normalized.Artifacts, journal.Artifacts)
	for index := range normalized.Artifacts {
		if normalized.Artifacts[index].Rooted != nil {
			location := *normalized.Artifacts[index].Rooted
			location.RemovalAttempted = false
			normalized.Artifacts[index].Rooted = &location
		}
	}
	normalized.Forests = make(
		[]applyPatchTransactionJournalForest,
		len(journal.Forests),
	)
	copy(normalized.Forests, journal.Forests)
	for index := range normalized.Forests {
		forest := &normalized.Forests[index]
		forest.StageRoot.RemovalAttempted = false
		forest.RollbackRoot.RemovalAttempted = false
		forest.SentinelWitness.RemovalAttempted = false
		forest.Entries = append(
			[]applyPatchTransactionJournalForestEntry(nil),
			journal.Forests[index].Entries...,
		)
		for entryIndex := range forest.Entries {
			forest.Entries[entryIndex].RemovalAttempted = false
		}
	}
	encoded, err := encodeApplyPatchTransactionJournal(key, &normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (store *applyPatchTxnStore) cleanupOwnedStateAuthenticated(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	if err := store.preparePrivateCleanup(key, journal); err != nil {
		return err
	}
	return store.finishCommittedStateCleanup()
}

func (store *applyPatchTxnStore) quarantineCommittedDirectory() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.revalidateLocked(); err != nil {
		return err
	}
	if strings.HasPrefix(store.activeName, applyPatchTransactionCommitNamePrefix) {
		return nil
	}
	committedName := applyPatchTransactionCommitNamePrefix +
		strings.TrimPrefix(store.activeName, applyPatchTransactionActiveNamePrefix)
	workspacePath, err := store.workspace.directoryPath()
	if err != nil {
		return err
	}
	anchor, err := openApplyPatchTxnAnchor(workspacePath)
	if err != nil {
		return err
	}
	defer anchor.Close()
	if err := applyPatchTxnRenameNoReplace(
		anchor,
		store.activeName,
		anchor,
		committedName,
	); err != nil {
		return err
	}
	store.activeName = committedName
	return applyPatchTxnSyncDirectory(anchor)
}

func (store *applyPatchTxnStore) finishCommittedStateCleanup() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	if err := store.revalidateLocked(); err != nil {
		store.mu.Unlock()
		return err
	}
	ordered := make([]string, 0, len(store.owned))
	for name := range store.owned {
		if name != applyPatchTransactionJournalFile {
			ordered = append(ordered, name)
		}
	}
	if _, exists := store.owned[applyPatchTransactionJournalFile]; exists {
		ordered = append(ordered, applyPatchTransactionJournalFile)
	}
	for _, name := range ordered {
		if err := removeApplyPatchTxnRootIdentity(
			store.activeRoot,
			name,
			store.owned[name],
		); err != nil {
			store.mu.Unlock()
			return err
		}
		delete(store.owned, name)
		if err := syncApplyPatchTxnRootDirectory(store.activeRoot); err != nil {
			store.mu.Unlock()
			return err
		}
	}
	entries, err := readApplyPatchTxnRootEntries(store.activeRoot)
	if err != nil || len(entries) != 0 {
		store.mu.Unlock()
		return errors.Join(
			errors.New("apply-patch committed directory is not empty"),
			err,
		)
	}
	root := store.activeRoot
	store.activeRoot = nil
	store.closed = true
	directoryInfo := store.activeInfo
	directoryName := store.activeName
	pointerInfo := store.pointerInfo
	workspace := store.workspace
	store.mu.Unlock()
	if err := root.Close(); err != nil {
		return err
	}
	return workspace.withDirectoryAnchor(func(workspaceRoot *os.Root) error {
		current, err := workspaceRoot.Lstat(directoryName)
		if err != nil || !current.IsDir() || !os.SameFile(current, directoryInfo) {
			return errors.Join(
				errors.New("apply-patch committed directory changed before removal"),
				err,
			)
		}
		if err := workspaceRoot.Remove(directoryName); err != nil {
			return err
		}
		if err := syncApplyPatchTxnRootDirectory(workspaceRoot); err != nil {
			return err
		}
		if pointerInfo == nil {
			return errors.New("apply-patch committed pointer identity is unavailable")
		}
		if err := removeApplyPatchTransactionExactRootEntry(
			workspaceRoot,
			applyPatchTransactionPointerFile,
			pointerInfo,
		); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(workspaceRoot)
	})
}

func createApplyPatchTxnStore(
	workspace *applyPatchTransactionWorkspaceState,
	intent *applyPatchTxnIntentPlan,
) (*applyPatchTxnStore, error) {
	if workspace == nil || intent == nil ||
		!strings.HasPrefix(intent.activeName, applyPatchTransactionActiveNamePrefix) ||
		validateApplyPatchTxnBasename(intent.activeName) != nil {
		return nil, errors.New("apply-patch transaction state store is invalid")
	}
	if _, err := workspace.directoryPath(); err != nil {
		return nil, err
	}
	store := &applyPatchTxnStore{
		workspace: workspace, activeName: intent.activeName,
		owned: make(map[string]applyPatchTxnIdentity),
	}
	var createdInfo os.FileInfo
	err := workspace.withDirectoryAnchor(func(root *os.Root) error {
		if err := requireApplyPatchTxnWorkspaceReadyForNewTransaction(root); err != nil {
			return err
		}
		if err := root.Mkdir(intent.activeName, 0o700); err != nil {
			return fmt.Errorf("create apply-patch transaction active directory: %w", err)
		}
		info, err := root.Lstat(intent.activeName)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.Join(
				errors.New("apply-patch transaction active directory is invalid"),
				err,
			)
		}
		createdInfo = info
		chmodErr := root.Chmod(intent.activeName, 0o700)
		if chmodErr != nil {
			return fmt.Errorf("secure apply-patch transaction active directory: %w", chmodErr)
		}
		syncErr := syncApplyPatchTxnRootDirectory(root)
		if syncErr != nil {
			return syncErr
		}
		info, err = root.Lstat(intent.activeName)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.Join(
				errors.New("apply-patch transaction active directory is invalid"),
				err,
			)
		}
		validationErr := validateApplyPatchTransactionPrivateObject(info, true)
		if validationErr != nil {
			return validationErr
		}
		activeRoot, err := root.OpenRoot(intent.activeName)
		if err != nil {
			return err
		}
		anchored, err := activeRoot.Lstat(".")
		if err != nil || !os.SameFile(info, anchored) {
			_ = activeRoot.Close()
			return errors.Join(
				errors.New("apply-patch transaction active directory changed while opening"),
				err,
			)
		}
		store.activeRoot = activeRoot
		store.activeInfo = info
		return nil
	})
	if err != nil {
		cleanupErr := cleanupApplyPatchTxnJournalLessActiveDirectory(
			workspace,
			intent.activeName,
			createdInfo,
		)
		return nil, errors.Join(err, cleanupErr)
	}
	return store, nil
}

func cleanupApplyPatchTxnJournalLessActiveDirectory(
	workspace *applyPatchTransactionWorkspaceState,
	name string,
	expected os.FileInfo,
) error {
	if expected == nil {
		return nil
	}
	return workspace.withDirectoryAnchor(func(root *os.Root) error {
		current, err := root.Lstat(name)
		if err != nil || !current.IsDir() || !os.SameFile(current, expected) {
			return errors.Join(
				errors.New("apply-patch journal-less active directory changed"),
				err,
			)
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		entries, entriesErr := readApplyPatchTxnRootEntries(child)
		closeErr := child.Close()
		if entriesErr != nil || closeErr != nil || len(entries) != 0 {
			return errors.Join(
				errors.New("apply-patch journal-less active directory is not empty"),
				entriesErr,
				closeErr,
			)
		}
		if err := root.Remove(name); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(root)
	})
}

func requireApplyPatchTxnWorkspaceReadyForNewTransaction(root *os.Root) error {
	if root == nil {
		return errors.New("apply-patch transaction workspace state is unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(applyPatchTransactionMaxEntries + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if len(entries) > applyPatchTransactionMaxEntries {
		return errors.New("apply-patch transaction workspace state has too many entries")
	}
	for _, entry := range entries {
		switch entry.Name() {
		case applyPatchTransactionWorkspaceBindingFile,
			applyPatchTransactionWorkspaceLockFile:
			continue
		default:
			return errors.New(
				"apply-patch transaction recovery is required before a new patch",
			)
		}
	}
	return nil
}

func (store *applyPatchTxnStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	root := store.activeRoot
	store.activeRoot = nil
	if root == nil {
		return nil
	}
	return root.Close()
}

func (store *applyPatchTxnStore) revalidateLocked() error {
	if store == nil || store.closed || store.activeRoot == nil ||
		store.workspace == nil || store.activeInfo == nil {
		return errors.New("apply-patch transaction active state is closed")
	}
	return store.workspace.withDirectoryAnchor(func(root *os.Root) error {
		info, err := root.Lstat(store.activeName)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(info, store.activeInfo) {
			return errors.Join(
				errors.New("apply-patch transaction active directory changed"),
				err,
			)
		}
		anchored, err := store.activeRoot.Lstat(".")
		if err != nil || !os.SameFile(anchored, store.activeInfo) {
			return errors.Join(
				errors.New("apply-patch transaction active directory changed"),
				err,
			)
		}
		return validateApplyPatchTransactionPrivateObject(info, true)
	})
}

func (store *applyPatchTxnStore) writeJournal(
	key []byte,
	journal *applyPatchTransactionJournal,
	faults ...func(string) error,
) error {
	encoded, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.revalidateLocked(); err != nil {
		return err
	}
	if err := store.revalidateCurrentJournalLocked(key); err != nil {
		return err
	}
	var fault func(string) error
	if len(faults) > 0 {
		fault = faults[0]
	}
	if fault != nil {
		if err := fault("journal_replace_before_rename"); err != nil {
			return err
		}
	}
	if err := store.writeReplacingFileLocked(
		applyPatchTransactionJournalStageFile,
		applyPatchTransactionJournalFile,
		encoded,
		fault,
	); err != nil {
		return err
	}
	store.journalBytes = append(store.journalBytes[:0], encoded...)
	return nil
}

func (store *applyPatchTxnStore) revalidateCurrentJournalLocked(key []byte) error {
	if len(store.journalBytes) == 0 {
		if _, err := store.activeRoot.Lstat(applyPatchTransactionJournalFile); err == nil ||
			!errors.Is(err, os.ErrNotExist) {
			return errors.Join(
				errors.New("apply-patch transaction journal appeared unexpectedly"),
				err,
			)
		}
		return nil
	}
	data, info, err := readApplyPatchTransactionPrivateRegularBounded(
		store.activeRoot,
		applyPatchTransactionJournalFile,
		applyPatchTransactionJournalMaxBytes,
	)
	if err != nil || !bytes.Equal(data, store.journalBytes) {
		return errors.Join(
			errors.New("apply-patch transaction journal changed before replacement"),
			err,
		)
	}
	if _, err := decodeApplyPatchTransactionJournal(key, data); err != nil {
		return err
	}
	expected, tracked := store.owned[applyPatchTransactionJournalFile]
	identity, identityErr := applyPatchTxnIdentityFromFileInfo(info, "regular")
	if !tracked || identityErr != nil || !identity.equal(expected) {
		return errors.Join(
			errors.New("apply-patch transaction journal identity changed before replacement"),
			identityErr,
		)
	}
	return nil
}

func (store *applyPatchTxnStore) readJournal(
	key []byte,
) (*applyPatchTransactionJournal, []byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.revalidateLocked(); err != nil {
		return nil, nil, err
	}
	data, _, err := readApplyPatchTransactionPrivateRegularBounded(
		store.activeRoot,
		applyPatchTransactionJournalFile,
		applyPatchTransactionJournalMaxBytes,
	)
	if err != nil {
		return nil, nil, err
	}
	journal, err := decodeApplyPatchTransactionJournal(key, data)
	if err != nil {
		return nil, nil, err
	}
	store.journalBytes = append(store.journalBytes[:0], data...)
	return journal, data, nil
}

func (store *applyPatchTxnStore) writeReplacingFileLocked(
	stageName string,
	targetName string,
	data []byte,
	fault func(string) error,
) error {
	if len(data) == 0 || len(data) > applyPatchTransactionStateFileMaxBytes ||
		validateApplyPatchTxnBasename(stageName) != nil ||
		validateApplyPatchTxnBasename(targetName) != nil {
		return errors.New("apply-patch transaction state write is invalid")
	}
	if _, err := store.activeRoot.Lstat(stageName); err == nil ||
		!errors.Is(err, os.ErrNotExist) {
		return errors.Join(
			errors.New("apply-patch transaction state stage is not absent"),
			err,
		)
	}
	file, err := store.activeRoot.OpenFile(
		stageName,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if err != nil {
		return err
	}
	stageInfo, statErr := file.Stat()
	var stageIdentity applyPatchTxnIdentity
	if statErr == nil {
		stageIdentity, statErr = applyPatchTxnIdentityFromFileInfo(stageInfo, "regular")
		if statErr == nil {
			store.owned[stageName] = stageIdentity
		}
	}
	writeErr := writeApplyPatchTransactionSyncedFile(file, data)
	closeErr := file.Close()
	if statErr != nil || writeErr != nil || closeErr != nil {
		cleanupErr := removeApplyPatchTransactionExactRootEntry(
			store.activeRoot,
			stageName,
			stageInfo,
		)
		if cleanupErr == nil {
			delete(store.owned, stageName)
		}
		return errors.Join(statErr, writeErr, closeErr, cleanupErr)
	}
	if err := syncApplyPatchTxnRootDirectory(store.activeRoot); err != nil {
		return err
	}
	if err := store.activeRoot.Rename(stageName, targetName); err != nil {
		cleanupErr := removeApplyPatchTransactionExactRootEntry(
			store.activeRoot,
			stageName,
			stageInfo,
		)
		if cleanupErr == nil {
			delete(store.owned, stageName)
		}
		return errors.Join(
			err,
			cleanupErr,
		)
	}
	delete(store.owned, stageName)
	store.owned[targetName] = stageIdentity
	if fault != nil {
		if err := fault("journal_replace_visible_before_sync"); err != nil {
			return err
		}
	}
	return syncApplyPatchTxnRootDirectory(store.activeRoot)
}

func (store *applyPatchTxnStore) writeBackups(
	ctx context.Context,
	key []byte,
	intent *applyPatchTxnIntentPlan,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if intent == nil || journal == nil || checkpoint == nil {
		return errors.New("apply-patch transaction backup state is invalid")
	}
	for _, operation := range intent.operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if operation.planned.source == nil {
			continue
		}
		artifact, err := requireApplyPatchTxnArtifact(
			journal,
			operation.index,
			applyPatchTransactionArtifactBackupBlob,
		)
		if err != nil || artifact.Backup == nil {
			return errors.Join(
				errors.New("apply-patch transaction backup artifact is unavailable"),
				err,
			)
		}
		data := operation.planned.source.data
		if err := verifyApplyPatchTransactionBackup(
			key,
			journal.TransactionID,
			artifact.StateName,
			*artifact.Backup,
			data,
		); err != nil {
			return err
		}
		if err := store.writeOneBackup(
			ctx,
			key,
			artifact,
			data,
			journal,
			checkpoint,
		); err != nil {
			return err
		}
	}
	return nil
}

func (store *applyPatchTxnStore) verifyBackups(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	return store.verifyBackupsWithPreparingDeletionOwnership(key, journal, false)
}

func (store *applyPatchTxnStore) verifyRecoveryBackups(
	key []byte,
	journal *applyPatchTransactionJournal,
) error {
	return store.verifyBackupsWithPreparingDeletionOwnership(
		key,
		journal,
		journal != nil && journal.Phase == applyPatchTransactionPhasePreparing,
	)
}

func (store *applyPatchTxnStore) verifyBackupsWithPreparingDeletionOwnership(
	key []byte,
	journal *applyPatchTransactionJournal,
	allowPreparingPartial bool,
) error {
	if journal == nil {
		return errors.New("apply-patch transaction journal is unavailable")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.revalidateLocked(); err != nil {
		return err
	}
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if artifact.Role != applyPatchTransactionArtifactBackupBlob {
			continue
		}
		if artifact.Backup == nil {
			return errors.New("apply-patch transaction backup record is unavailable")
		}
		if artifact.StateIdentity == nil {
			_, inspectErr := store.activeRoot.Lstat(artifact.StateName)
			if errors.Is(inspectErr, os.ErrNotExist) {
				continue
			}
			return errors.New("apply-patch transaction backup is not checkpointed")
		}
		if artifact.StateLinks != 1 {
			return errors.New("apply-patch transaction backup link state is invalid")
		}
		data, info, err := readApplyPatchTransactionPrivateRegularBounded(
			store.activeRoot,
			artifact.StateName,
			applyPatchTransactionMaxBackupBytes,
		)
		if errors.Is(err, os.ErrNotExist) && store.committedCleanupAuthenticated {
			continue
		}
		if err != nil {
			return err
		}
		identity, err := applyPatchTxnIdentityFromFileInfo(info, "regular")
		if err != nil || !identity.equal(*artifact.StateIdentity) {
			return errors.Join(
				errors.New("apply-patch transaction backup identity changed"),
				err,
			)
		}
		state, inspectErr := store.inspectActiveRegular(artifact.StateName)
		if inspectErr != nil || !state.Identity.equal(identity) || state.Links != 1 {
			return errors.Join(
				errors.New("apply-patch transaction backup link state changed"),
				inspectErr,
			)
		}
		if allowPreparingPartial {
			// The authenticated identity checkpoint is the ownership transition
			// for an exclusively created private backup. Preparing recovery only
			// deletes this exact nlink=1 object; it never reads it for rollback.
			continue
		}
		if err := verifyApplyPatchTransactionBackup(
			key,
			journal.TransactionID,
			artifact.StateName,
			*artifact.Backup,
			data,
		); err != nil {
			return err
		}
	}
	return nil
}

func (store *applyPatchTxnStore) inspectActiveRegular(
	name string,
) (applyPatchTxnObjectState, error) {
	if store == nil || store.workspace == nil || store.activeName == "" {
		return applyPatchTxnObjectState{}, errors.New(
			"apply-patch transaction active state is unavailable",
		)
	}
	workspacePath, err := store.workspace.directoryPath()
	if err != nil {
		return applyPatchTxnObjectState{}, err
	}
	anchor, err := openApplyPatchTxnAnchor(filepath.Join(workspacePath, store.activeName))
	if err != nil {
		return applyPatchTxnObjectState{}, err
	}
	defer anchor.Close()
	expected, err := applyPatchTxnIdentityFromFileInfo(store.activeInfo, "directory")
	if err != nil || !anchor.identity.equal(expected) {
		return applyPatchTxnObjectState{}, errors.Join(
			errors.New("apply-patch transaction active state identity changed"),
			err,
		)
	}
	return applyPatchTxnInspectAt(anchor, name)
}

func (store *applyPatchTxnStore) readBackup(
	key []byte,
	journal *applyPatchTransactionJournal,
	operationIndex int,
) ([]byte, error) {
	if journal == nil {
		return nil, errors.New("apply-patch transaction journal is unavailable")
	}
	artifact, err := requireApplyPatchTxnArtifact(
		journal,
		operationIndex,
		applyPatchTransactionArtifactBackupBlob,
	)
	if err != nil || artifact.Backup == nil || artifact.StateIdentity == nil ||
		artifact.StateLinks != 1 {
		return nil, errors.Join(
			errors.New("apply-patch authenticated backup is unavailable"),
			err,
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	revalidationErr := store.revalidateLocked()
	if revalidationErr != nil {
		return nil, revalidationErr
	}
	data, info, err := readApplyPatchTransactionPrivateRegularBounded(
		store.activeRoot,
		artifact.StateName,
		applyPatchTransactionMaxBackupBytes,
	)
	if err != nil {
		return nil, err
	}
	identity, err := applyPatchTxnIdentityFromFileInfo(info, "regular")
	if err != nil || !identity.equal(*artifact.StateIdentity) {
		return nil, errors.Join(
			errors.New("apply-patch authenticated backup identity changed"),
			err,
		)
	}
	if err := verifyApplyPatchTransactionBackup(
		key,
		journal.TransactionID,
		artifact.StateName,
		*artifact.Backup,
		data,
	); err != nil {
		return nil, err
	}
	return data, nil
}

func (store *applyPatchTxnStore) writeOneBackup(
	ctx context.Context,
	key []byte,
	artifact *applyPatchTransactionJournalArtifact,
	data []byte,
	journal *applyPatchTransactionJournal,
	checkpoint applyPatchTxnJournalCheckpoint,
) error {
	store.mu.Lock()
	if err := store.revalidateLocked(); err != nil {
		store.mu.Unlock()
		return err
	}
	file, err := store.activeRoot.OpenFile(
		artifact.StateName,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if err != nil {
		store.mu.Unlock()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		store.mu.Unlock()
		return err
	}
	identity, err := applyPatchTxnIdentityFromFileInfo(info, "regular")
	if err != nil {
		_ = file.Close()
		store.mu.Unlock()
		return err
	}
	artifact.StateIdentity = copyApplyPatchTxnIdentity(identity)
	artifact.StateLinks = 1
	store.owned[artifact.StateName] = identity
	initialSyncErr := syncApplyPatchTxnRootDirectory(store.activeRoot)
	if initialSyncErr != nil {
		_ = file.Close()
		store.mu.Unlock()
		return initialSyncErr
	}
	store.mu.Unlock()
	checkpointErr := checkpoint(journal)
	if checkpointErr != nil {
		_ = file.Close()
		return checkpointErr
	}
	writeErr := applyPatchTxnWriteRegularContext(ctx, file, data, 0o600, true)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	store.mu.Lock()
	revalidationErr := store.revalidateLocked()
	if revalidationErr != nil {
		store.mu.Unlock()
		return revalidationErr
	}
	finalSyncErr := syncApplyPatchTxnRootDirectory(store.activeRoot)
	if finalSyncErr != nil {
		store.mu.Unlock()
		return finalSyncErr
	}
	readData, readInfo, err := readApplyPatchTransactionPrivateRegularBounded(
		store.activeRoot,
		artifact.StateName,
		applyPatchTransactionMaxBackupBytes,
	)
	if err != nil || !os.SameFile(info, readInfo) ||
		len(readData) != len(data) {
		store.mu.Unlock()
		return errors.Join(
			errors.New("apply-patch transaction backup changed after writing"),
			err,
		)
	}
	if err := verifyApplyPatchTransactionBackup(
		key,
		journal.TransactionID,
		artifact.StateName,
		*artifact.Backup,
		readData,
	); err != nil {
		store.mu.Unlock()
		return err
	}
	store.mu.Unlock()
	return checkpoint(journal)
}

func readApplyPatchTransactionPrivateRegularBounded(
	root *os.Root,
	name string,
	limit int,
) ([]byte, os.FileInfo, error) {
	if root == nil || limit < 0 || validateApplyPatchTxnBasename(name) != nil {
		return nil, nil, errors.New("apply-patch transaction private file is invalid")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > int64(limit) {
		return nil, nil, errors.New("apply-patch transaction private file is invalid")
	}
	validationErr := validateApplyPatchTransactionPrivateObject(info, false)
	if validationErr != nil {
		return nil, nil, validationErr
	}
	file, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private file changed while opening"),
			statErr,
		)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > limit {
		return nil, nil, errors.Join(readErr, closeErr)
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) || current.Mode() != info.Mode() ||
		current.Size() != info.Size() {
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private file changed while reading"),
			err,
		)
	}
	return data, info, nil
}

func syncApplyPatchTxnRootDirectory(root *os.Root) error {
	if root == nil {
		return errors.New("apply-patch transaction directory is unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func removeApplyPatchTxnRootIdentity(
	root *os.Root,
	name string,
	expected applyPatchTxnIdentity,
) error {
	if root == nil || !expected.valid("regular") ||
		validateApplyPatchTxnBasename(name) != nil {
		return errors.New("apply-patch transaction owned state identity is invalid")
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 {
		return errors.Join(
			errors.New("apply-patch transaction owned state file changed"),
			err,
		)
	}
	identity, err := applyPatchTxnIdentityFromFileInfo(current, "regular")
	if err != nil || !identity.equal(expected) {
		return errors.Join(
			errors.New("apply-patch transaction owned state file changed"),
			err,
		)
	}
	return root.Remove(name)
}

func readApplyPatchTxnRootEntries(root *os.Root) ([]os.DirEntry, error) {
	if root == nil {
		return nil, errors.New("apply-patch transaction directory is unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(applyPatchTransactionMaxEntries + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(entries) > applyPatchTransactionMaxEntries {
		return nil, errors.New("apply-patch transaction directory has too many entries")
	}
	return entries, nil
}
