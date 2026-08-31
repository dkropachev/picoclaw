package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	workflowDevelopmentPublishJournalVersion       = 3
	workflowDevelopmentPublishLegacyJournalVersion = 2
	workflowDevelopmentPublishJournalFile          = "publish-transaction.json"

	workflowDevelopmentPublishPhasePrepared  = "prepared"
	workflowDevelopmentPublishPhaseCommitted = "committed"

	workflowDevelopmentPublishStagePrepared             = "prepared"
	workflowDevelopmentPublishStageTargetWriteStarted   = "target_write_started"
	workflowDevelopmentPublishStageManifestWriteStarted = "manifest_write_started"
	workflowDevelopmentPublishStageArchiveWriteStarted  = "archive_write_started"
	workflowDevelopmentPublishStageActiveRemoveStarted  = "active_remove_started"
	workflowDevelopmentPublishStageCommitted            = "committed"
)

var (
	ErrWorkflowDevelopmentPublishRollbackFailed = errors.New(
		"workflow development publish rollback failed",
	)
	ErrWorkflowDevelopmentPublishRecoveryFailed = errors.New(
		"workflow development publish recovery failed",
	)
)

type workflowDevelopmentPublishFileSnapshot struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type workflowDevelopmentPublishFileTransition struct {
	Preimage  workflowDevelopmentPublishFileSnapshot `json:"preimage"`
	Postimage workflowDevelopmentPublishFileSnapshot `json:"postimage"`
}

type workflowDevelopmentPublishJournal struct {
	Version        int                                      `json:"version"`
	Phase          string                                   `json:"phase"`
	Stage          string                                   `json:"stage"`
	DefinitionsDir string                                   `json:"definitions_dir"`
	TargetRef      string                                   `json:"target_ref"`
	SessionID      string                                   `json:"session_id"`
	Target         workflowDevelopmentPublishFileTransition `json:"target"`
	Manifest       workflowDevelopmentPublishFileTransition `json:"manifest"`
	Active         workflowDevelopmentPublishFileTransition `json:"active"`
	Archive        workflowDevelopmentPublishFileTransition `json:"archive"`
}

type workflowDevelopmentPublishBoundary string

const (
	workflowDevelopmentPublishBoundaryPrepared          workflowDevelopmentPublishBoundary = "prepared"
	workflowDevelopmentPublishBoundaryTargetWritten     workflowDevelopmentPublishBoundary = "target_written"
	workflowDevelopmentPublishBoundaryManifestActivated workflowDevelopmentPublishBoundary = "manifest_activated"
	workflowDevelopmentPublishBoundarySessionArchived   workflowDevelopmentPublishBoundary = "session_archived"
	workflowDevelopmentPublishBoundaryActiveRemoved     workflowDevelopmentPublishBoundary = "active_removed"
	workflowDevelopmentPublishBoundaryCommitted         workflowDevelopmentPublishBoundary = "committed"
)

// workflowDevelopmentPublishHooks exists only to make transaction boundaries
// deterministically testable. Production callers always pass nil.
type workflowDevelopmentPublishHooks struct {
	afterBoundary       func(workflowDevelopmentPublishBoundary) error
	leaveJournalOnError bool
	writeJournal        workflowDevelopmentPublishJournalWriter
}

type workflowDevelopmentPublishJournalWriter func(
	string,
	*workflowDevelopmentPublishJournal,
) error

// PublishWorkflowDevelopmentFenced publishes only if every optimistic
// concurrency fence still matches while the workspace mutation lock is held.
// If gate is non-nil, it is re-run under that same lock against the exact
// persisted draft and must return the expected dependency revision as ready.
func PublishWorkflowDevelopmentFenced(
	ctx context.Context,
	workspace string,
	request WorkflowDevelopmentPublishRequest,
	runtime RuntimeCompatibility,
	gate WorkflowDevelopmentPublishGate,
	opts ...LocalOption,
) (*WorkflowDevelopmentPublishResult, error) {
	return publishWorkflowDevelopmentTransaction(
		ctx,
		workspace,
		&request,
		runtime,
		gate,
		nil,
		opts...,
	)
}

func checkWorkflowDevelopmentPublishGate(
	ctx context.Context,
	request WorkflowDevelopmentPublishRequest,
	session *WorkflowDevelopmentSession,
	workflow *Workflow,
	gate WorkflowDevelopmentPublishGate,
) error {
	if gate == nil {
		if request.ExpectedDependencyRevision != "" {
			return ErrWorkflowDevelopmentPublishGateRequired
		}
		return nil
	}
	gateResult, err := gate(ctx, WorkflowDevelopmentPublishGateInput{
		WorkflowRef:   session.TargetWorkflowRef,
		DraftRevision: session.DraftRevision,
		YAML:          session.YAML,
		Workflow:      workflow,
	})
	if err != nil {
		return err
	}
	if request.ExpectedDependencyRevision == "" ||
		gateResult.Revision == "" ||
		request.ExpectedDependencyRevision != gateResult.Revision {
		return ErrWorkflowDevelopmentDependencyRevisionMismatch
	}
	if !gateResult.Ready {
		return ErrWorkflowDevelopmentPublishNotReady
	}
	return nil
}

func workflowDevelopmentArchivePath(workspace, sessionID string) string {
	return filepath.Join(
		workspace,
		workflowDevelopmentDir,
		"archive",
		safeID(sessionID)+".json",
	)
}

func workflowDevelopmentPublishSnapshot(
	snapshot workflowTemplateFileSnapshot,
) workflowDevelopmentPublishFileSnapshot {
	return workflowDevelopmentPublishFileSnapshot{
		Exists: snapshot.exists,
		Data:   snapshot.data,
		Mode:   uint32(snapshot.mode.Perm()),
	}
}

func workflowDevelopmentPublishTransition(
	preimage workflowTemplateFileSnapshot,
	postimage workflowTemplateFileSnapshot,
) workflowDevelopmentPublishFileTransition {
	return workflowDevelopmentPublishFileTransition{
		Preimage:  workflowDevelopmentPublishSnapshot(preimage),
		Postimage: workflowDevelopmentPublishSnapshot(postimage),
	}
}

func workflowDevelopmentPublishSnapshotRevision(
	snapshot workflowTemplateFileSnapshot,
) string {
	if !snapshot.exists {
		return WorkflowTargetRevisionMissing
	}
	return workflowContentRevision(snapshot.data)
}

func (snapshot workflowDevelopmentPublishFileSnapshot) fileSnapshot(
	path string,
) workflowTemplateFileSnapshot {
	return workflowTemplateFileSnapshot{
		path:   path,
		exists: snapshot.Exists,
		data:   snapshot.Data,
		mode:   fs.FileMode(snapshot.Mode).Perm(),
	}
}

func workflowDevelopmentPublishJournalPath(workspace string) string {
	return filepath.Join(
		workspace,
		workflowMutationStateDir,
		workflowDevelopmentPublishJournalFile,
	)
}

func writeWorkflowDevelopmentPublishJournal(
	workspace string,
	journal *workflowDevelopmentPublishJournal,
) error {
	if err := validateWorkflowDevelopmentPublishJournal(journal); err != nil {
		return err
	}
	data, marshalErr := json.MarshalIndent(journal, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	path, pathErr := checkedWorkflowDevelopmentPublishJournalPath(workspace)
	if pathErr != nil {
		return pathErr
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeWorkflowTemplateAtomic(path, data, 0o600)
}

func writeWorkflowDevelopmentPublishCommitWithRetry(
	workspace string,
	journal *workflowDevelopmentPublishJournal,
	writeJournal workflowDevelopmentPublishJournalWriter,
) error {
	if writeJournal == nil {
		writeJournal = writeWorkflowDevelopmentPublishJournal
	}
	firstErr := writeJournal(workspace, journal)
	if firstErr == nil {
		return nil
	}
	retryErr := writeJournal(workspace, journal)
	if retryErr == nil {
		return nil
	}
	return errors.Join(firstErr, retryErr)
}

func readWorkflowDevelopmentPublishJournal(
	workspace string,
) (*workflowDevelopmentPublishJournal, bool, error) {
	path, pathErr := checkedWorkflowDevelopmentPublishJournalPath(workspace)
	if pathErr != nil {
		return nil, false, pathErr
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, readErr
	}
	var journal workflowDevelopmentPublishJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, false, err
	}
	if err := validateWorkflowDevelopmentPublishJournal(&journal); err != nil {
		return nil, false, err
	}
	return &journal, false, nil
}

func validateWorkflowDevelopmentPublishJournal(
	journal *workflowDevelopmentPublishJournal,
) error {
	if journal == nil {
		return fmt.Errorf("workflow publish journal is required")
	}
	if journal.Version != workflowDevelopmentPublishJournalVersion &&
		journal.Version != workflowDevelopmentPublishLegacyJournalVersion {
		return fmt.Errorf("unsupported workflow publish journal version")
	}
	switch journal.Phase {
	case workflowDevelopmentPublishPhasePrepared, workflowDevelopmentPublishPhaseCommitted:
	default:
		return fmt.Errorf("invalid workflow publish journal phase")
	}
	switch journal.Stage {
	case workflowDevelopmentPublishStagePrepared,
		workflowDevelopmentPublishStageTargetWriteStarted,
		workflowDevelopmentPublishStageManifestWriteStarted,
		workflowDevelopmentPublishStageArchiveWriteStarted,
		workflowDevelopmentPublishStageActiveRemoveStarted:
		if journal.Phase != workflowDevelopmentPublishPhasePrepared {
			return fmt.Errorf("invalid workflow publish journal stage")
		}
	case workflowDevelopmentPublishStageCommitted:
		if journal.Phase != workflowDevelopmentPublishPhaseCommitted {
			return fmt.Errorf("invalid workflow publish journal stage")
		}
	default:
		return fmt.Errorf("invalid workflow publish journal stage")
	}
	canonical, canonicalErr := CanonicalLocalRef(journal.TargetRef)
	if canonicalErr != nil || canonical != journal.TargetRef {
		return fmt.Errorf("invalid workflow publish journal target")
	}
	cleanDir, definitionsErr := cleanDefinitionsDir(journal.DefinitionsDir)
	if definitionsErr != nil || filepath.ToSlash(cleanDir) != journal.DefinitionsDir {
		return fmt.Errorf("invalid workflow publish journal definitions dir")
	}
	if strings.TrimSpace(journal.SessionID) == "" ||
		safeID(journal.SessionID) != journal.SessionID {
		return fmt.Errorf("invalid workflow publish journal session")
	}
	for _, snapshot := range []workflowDevelopmentPublishFileSnapshot{
		journal.Target.Preimage,
		journal.Target.Postimage,
		journal.Manifest.Preimage,
		journal.Manifest.Postimage,
		journal.Active.Preimage,
		journal.Active.Postimage,
		journal.Archive.Preimage,
		journal.Archive.Postimage,
	} {
		if err := validateWorkflowDevelopmentPublishFileSnapshot(snapshot); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowDevelopmentPublishFileSnapshot(
	snapshot workflowDevelopmentPublishFileSnapshot,
) error {
	if snapshot.Mode&^uint32(0o777) != 0 {
		return fmt.Errorf("invalid workflow publish journal file mode")
	}
	if !snapshot.Exists && (len(snapshot.Data) != 0 || snapshot.Mode != 0) {
		return fmt.Errorf("invalid workflow publish journal file state")
	}
	return nil
}

// recoverWorkflowDevelopmentPublishTransaction runs only while the caller owns
// the workspace mutation lock. Prepared transactions roll back to every
// captured pre-image; committed transactions only need journal cleanup.
func recoverWorkflowDevelopmentPublishFileTransactionLegacy(workspace string) error {
	journal, missing, readErr := readWorkflowDevelopmentPublishJournal(workspace)
	if readErr != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, readErr)
	}
	if missing {
		return nil
	}
	if journal.Phase == workflowDevelopmentPublishPhaseCommitted {
		if err := removeWorkflowDevelopmentPublishJournal(workspace); err != nil {
			return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
		}
		return nil
	}

	resolved, resolveErr := (Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: journal.DefinitionsDir,
	}).ResolveLocal(journal.TargetRef)
	if resolveErr != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, resolveErr)
	}
	archivePath, archivePathErr := checkedWorkflowDevelopmentArchivePath(
		workspace,
		journal.SessionID,
	)
	if archivePathErr != nil {
		return errors.Join(
			ErrWorkflowDevelopmentPublishRecoveryFailed,
			archivePathErr,
		)
	}
	manifestPath, manifestPathErr := checkedCompatibilityManifestPath(workspace)
	if manifestPathErr != nil {
		return errors.Join(
			ErrWorkflowDevelopmentPublishRecoveryFailed,
			manifestPathErr,
		)
	}
	activePath, activePathErr := checkedActiveDevelopmentPath(workspace)
	if activePathErr != nil {
		return errors.Join(
			ErrWorkflowDevelopmentPublishRecoveryFailed,
			activePathErr,
		)
	}
	transitions := []workflowFileRecoveryTransition{
		{
			label:     "compatibility manifest",
			path:      manifestPath,
			preimage:  journal.Manifest.Preimage.fileSnapshot(manifestPath),
			postimage: journal.Manifest.Postimage.fileSnapshot(manifestPath),
		},
		{
			label:     "published workflow target",
			path:      resolved.Path,
			preimage:  journal.Target.Preimage.fileSnapshot(resolved.Path),
			postimage: journal.Target.Postimage.fileSnapshot(resolved.Path),
		},
		{
			label:     "development archive",
			path:      archivePath,
			preimage:  journal.Archive.Preimage.fileSnapshot(archivePath),
			postimage: journal.Archive.Postimage.fileSnapshot(archivePath),
		},
		{
			label:     "active development session",
			path:      activePath,
			preimage:  journal.Active.Preimage.fileSnapshot(activePath),
			postimage: journal.Active.Postimage.fileSnapshot(activePath),
		},
	}
	if err := recoverWorkflowFileTransitions(transitions...); err != nil {
		return errors.Join(
			ErrWorkflowDevelopmentPublishRecoveryFailed,
			err,
		)
	}
	if err := removeWorkflowDevelopmentPublishJournal(workspace); err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	return nil
}

func workflowDevelopmentPublishJournalIsCommitted(workspace string) (bool, error) {
	journal, missing, readErr := readWorkflowDevelopmentPublishJournal(workspace)
	if readErr != nil || missing {
		return false, readErr
	}
	return journal.Phase == workflowDevelopmentPublishPhaseCommitted, nil
}

func removeWorkflowDevelopmentPublishJournal(workspace string) error {
	path, err := checkedWorkflowDevelopmentPublishJournalPath(workspace)
	if err != nil {
		return err
	}
	return removeWorkflowDevelopmentPublishFile(path)
}

func checkedWorkflowDevelopmentPublishJournalPath(
	workspace string,
) (string, error) {
	return resolveWorkflowInternalPath(
		workspace,
		workflowMutationStateDir,
		workflowDevelopmentPublishJournalFile,
	)
}

func removeWorkflowDevelopmentPublishFile(path string) error {
	if removeErr := fileutil.RemoveDurable(path); removeErr != nil {
		if errors.Is(removeErr, fs.ErrNotExist) {
			return nil
		}
		return removeErr
	}
	return nil
}
