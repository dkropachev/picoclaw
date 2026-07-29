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
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	workflowDevelopmentPublishJournalVersion = 2
	workflowDevelopmentPublishJournalFile    = "publish-transaction.json"

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

func publishWorkflowDevelopmentTransaction(
	ctx context.Context,
	workspace string,
	request *WorkflowDevelopmentPublishRequest,
	runtime RuntimeCompatibility,
	gate WorkflowDevelopmentPublishGate,
	hooks *workflowDevelopmentPublishHooks,
	opts ...LocalOption,
) (*WorkflowDevelopmentPublishResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session, sessionErr := requireActiveDevelopment(workspace)
	if sessionErr != nil {
		return nil, sessionErr
	}
	currentTargetRevision, revisionErr := captureWorkflowDevelopmentTargetRevision(
		workspace,
		session.TargetWorkflowRef,
		opts...,
	)
	if revisionErr != nil {
		return nil, revisionErr
	}
	if request == nil {
		if session.BaseTargetRevision == WorkflowTargetRevisionUnknown {
			session.BaseTargetRevision = currentTargetRevision
			session.UpdatedAt = time.Now().UTC()
			if err := writeActiveDevelopment(workspace, session); err != nil {
				return nil, err
			}
		}
		request = &WorkflowDevelopmentPublishRequest{
			SessionID:                  session.ID,
			ExpectedSessionRevision:    session.SessionRevision,
			ExpectedDraftRevision:      session.DraftRevision,
			ExpectedBaseTargetRevision: session.BaseTargetRevision,
		}
	}
	if err := checkWorkflowDevelopmentPublishRevisions(
		session,
		*request,
		currentTargetRevision,
	); err != nil {
		return nil, err
	}

	draftBytes := []byte(session.YAML)
	workflow, parseErr := Parse(draftBytes)
	if parseErr != nil {
		return nil, fmt.Errorf(
			"%w: parse exact workflow draft: %v",
			ErrWorkflowDevelopmentDraftNotReady,
			parseErr,
		)
	}
	if err := Validate(workflow); err != nil {
		return nil, fmt.Errorf(
			"%w: validate exact workflow draft: %v",
			ErrWorkflowDevelopmentDraftNotReady,
			err,
		)
	}
	if err := requireCurrentSuccessfulDevelopmentTest(session); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkflowDevelopmentDraftNotReady, err)
	}
	if err := checkWorkflowDevelopmentPublishGate(
		ctx,
		*request,
		session,
		workflow,
		gate,
	); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	local := collectLocalOptions(opts...)
	definitionsDir, definitionsErr := cleanDefinitionsDir(local.DefinitionsDir)
	if definitionsErr != nil {
		return nil, definitionsErr
	}
	resolved, resolveErr := local.resolver(workspace).ResolveLocal(session.TargetWorkflowRef)
	if resolveErr != nil {
		return nil, resolveErr
	}
	manifest, manifestErr := buildCompatibilityManifestLocked(
		ctx,
		workspace,
		runtime,
		&workflowCompatibilityOverlay{
			ref:  resolved.Canonical,
			data: draftBytes,
		},
		opts...,
	)
	if manifestErr != nil {
		return nil, manifestErr
	}
	if !templateHasValidCompatibilityStamp(manifest, resolved.Canonical) {
		return nil, fmt.Errorf("published workflow did not receive a valid compatibility stamp")
	}
	// Dependency checks and manifest preparation may be comparatively slow.
	// Re-read every authoring fence before taking transaction snapshots so an
	// out-of-band editor cannot win that window.
	latestSession, latestSessionErr := requireActiveDevelopment(workspace)
	if latestSessionErr != nil {
		return nil, latestSessionErr
	}
	latestTargetRevision, latestRevisionErr := captureWorkflowDevelopmentTargetRevision(
		workspace,
		latestSession.TargetWorkflowRef,
		opts...,
	)
	if latestRevisionErr != nil {
		return nil, latestRevisionErr
	}
	if err := checkWorkflowDevelopmentPublishRevisions(
		latestSession,
		*request,
		latestTargetRevision,
	); err != nil {
		return nil, err
	}
	// Manifest construction loads the reusable closure and can take long
	// enough for a non-cooperating editor to change a child definition. Fence
	// the exact dependency graph again immediately before snapshots and the
	// durable transaction begin.
	if err := checkWorkflowDevelopmentPublishGate(
		ctx,
		*request,
		latestSession,
		workflow,
		gate,
	); err != nil {
		return nil, err
	}
	manifestData, manifestMarshalErr := json.MarshalIndent(manifest, "", "  ")
	if manifestMarshalErr != nil {
		return nil, manifestMarshalErr
	}
	archiveData, archiveMarshalErr := marshalWorkflowDevelopmentArchive(session, "published")
	if archiveMarshalErr != nil {
		return nil, archiveMarshalErr
	}

	archivePath, archivePathErr := checkedWorkflowDevelopmentArchivePath(
		workspace,
		session.ID,
	)
	if archivePathErr != nil {
		return nil, archivePathErr
	}
	manifestPath, manifestPathErr := checkedCompatibilityManifestPath(workspace)
	if manifestPathErr != nil {
		return nil, manifestPathErr
	}
	activePath, activePathErr := checkedActiveDevelopmentPath(workspace)
	if activePathErr != nil {
		return nil, activePathErr
	}
	targetSnapshot, targetSnapshotErr := captureWorkflowTemplateFile(resolved.Path)
	if targetSnapshotErr != nil {
		return nil, targetSnapshotErr
	}
	if workflowDevelopmentPublishSnapshotRevision(targetSnapshot) !=
		session.BaseTargetRevision {
		return nil, ErrWorkflowTargetRevisionMismatch
	}
	manifestSnapshot, manifestSnapshotErr := captureWorkflowTemplateFile(manifestPath)
	if manifestSnapshotErr != nil {
		return nil, manifestSnapshotErr
	}
	activeSnapshot, activeSnapshotErr := captureWorkflowTemplateFile(activePath)
	if activeSnapshotErr != nil {
		return nil, activeSnapshotErr
	}
	if !activeSnapshot.exists {
		return nil, ErrNoActiveDevelopment
	}
	var snapshotSession WorkflowDevelopmentSession
	if err := json.Unmarshal(activeSnapshot.data, &snapshotSession); err != nil {
		return nil, err
	}
	if err := checkWorkflowDevelopmentPublishRevisions(
		&snapshotSession,
		*request,
		workflowDevelopmentPublishSnapshotRevision(targetSnapshot),
	); err != nil {
		return nil, err
	}
	archiveSnapshot, archiveSnapshotErr := captureWorkflowTemplateFile(archivePath)
	if archiveSnapshotErr != nil {
		return nil, archiveSnapshotErr
	}
	targetMode := fs.FileMode(0o644)
	if targetSnapshot.exists {
		targetMode = targetSnapshot.mode
	}
	targetPostimage := workflowTemplateFileSnapshot{
		path:   resolved.Path,
		exists: true,
		data:   draftBytes,
		mode:   targetMode,
	}
	manifestPostimage := workflowTemplateFileSnapshot{
		path:   manifestPath,
		exists: true,
		data:   manifestData,
		mode:   0o600,
	}
	archivePostimage := workflowTemplateFileSnapshot{
		path:   archivePath,
		exists: true,
		data:   archiveData,
		mode:   0o600,
	}
	activePostimage := workflowTemplateFileSnapshot{path: activePath}

	journal := &workflowDevelopmentPublishJournal{
		Version:        workflowDevelopmentPublishJournalVersion,
		Phase:          workflowDevelopmentPublishPhasePrepared,
		Stage:          workflowDevelopmentPublishStagePrepared,
		DefinitionsDir: filepath.ToSlash(definitionsDir),
		TargetRef:      resolved.Canonical,
		SessionID:      session.ID,
		Target: workflowDevelopmentPublishTransition(
			targetSnapshot,
			targetPostimage,
		),
		Manifest: workflowDevelopmentPublishTransition(
			manifestSnapshot,
			manifestPostimage,
		),
		Active: workflowDevelopmentPublishTransition(
			activeSnapshot,
			activePostimage,
		),
		Archive: workflowDevelopmentPublishTransition(
			archiveSnapshot,
			archivePostimage,
		),
	}
	writeJournal := workflowDevelopmentPublishJournalWriter(
		writeWorkflowDevelopmentPublishJournal,
	)
	if hooks != nil && hooks.writeJournal != nil {
		writeJournal = hooks.writeJournal
	}
	if err := writeJournal(workspace, journal); err != nil {
		if recoveryErr := recoverWorkflowDevelopmentPublishTransaction(workspace); recoveryErr != nil {
			return nil, errors.Join(err, recoveryErr)
		}
		return nil, err
	}
	fail := func(publishErr error) (*WorkflowDevelopmentPublishResult, error) {
		if hooks != nil && hooks.leaveJournalOnError {
			return nil, publishErr
		}
		if rollbackErr := recoverWorkflowDevelopmentPublishTransaction(workspace); rollbackErr != nil {
			return nil, errors.Join(
				publishErr,
				ErrWorkflowDevelopmentPublishRollbackFailed,
				rollbackErr,
			)
		}
		return nil, publishErr
	}
	checkBoundary := func(boundary workflowDevelopmentPublishBoundary) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hooks != nil && hooks.afterBoundary != nil {
			return hooks.afterBoundary(boundary)
		}
		return nil
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundaryPrepared); err != nil {
		return fail(err)
	}

	journal.Stage = workflowDevelopmentPublishStageTargetWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(resolved.Path), 0o755); err != nil {
		return fail(err)
	}
	if err := writeWorkflowTemplateAtomic(resolved.Path, draftBytes, targetMode); err != nil {
		return fail(err)
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundaryTargetWritten); err != nil {
		return fail(err)
	}

	journal.Stage = workflowDevelopmentPublishStageManifestWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(manifestPath), 0o755); err != nil {
		return fail(err)
	}
	if err := writeWorkflowTemplateAtomic(
		manifestPath,
		manifestData,
		0o600,
	); err != nil {
		return fail(err)
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundaryManifestActivated); err != nil {
		return fail(err)
	}

	journal.Stage = workflowDevelopmentPublishStageArchiveWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(archivePath), 0o755); err != nil {
		return fail(err)
	}
	if err := writeWorkflowTemplateAtomic(archivePath, archiveData, 0o600); err != nil {
		return fail(err)
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundarySessionArchived); err != nil {
		return fail(err)
	}

	journal.Stage = workflowDevelopmentPublishStageActiveRemoveStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := removeWorkflowDevelopmentPublishFile(activePath); err != nil {
		return fail(err)
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundaryActiveRemoved); err != nil {
		return fail(err)
	}

	journal.Phase = workflowDevelopmentPublishPhaseCommitted
	journal.Stage = workflowDevelopmentPublishStageCommitted
	if err := writeWorkflowDevelopmentPublishCommitWithRetry(
		workspace,
		journal,
		writeJournal,
	); err != nil {
		// A visible marker is not durability proof after a failed sync. Leave
		// the ambiguous journal untouched and return an error. Recovery under
		// the next mutation lock will either finalize a durable committed marker
		// or roll back the durable prepared marker.
		return nil, err
	}
	if err := checkBoundary(workflowDevelopmentPublishBoundaryCommitted); err != nil {
		if hooks != nil && hooks.leaveJournalOnError {
			return nil, err
		}
		_ = removeWorkflowDevelopmentPublishJournal(workspace)
		return &WorkflowDevelopmentPublishResult{
			WorkflowRef: session.TargetWorkflowRef,
			Session:     session,
		}, nil
	}

	// Once the committed marker is durable, cleanup is retryable housekeeping:
	// a leftover committed journal is finalized by the next mutation.
	_ = removeWorkflowDevelopmentPublishJournal(workspace)
	return &WorkflowDevelopmentPublishResult{
		WorkflowRef: session.TargetWorkflowRef,
		Session:     session,
	}, nil
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

func marshalWorkflowDevelopmentArchive(
	session *WorkflowDevelopmentSession,
	state string,
) ([]byte, error) {
	if session == nil {
		return nil, ErrNoActiveDevelopment
	}
	copySession := *session
	copySession.Status = strings.TrimSpace(state)
	copySession.UpdatedAt = time.Now().UTC()
	return json.MarshalIndent(copySession, "", "  ")
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
	if journal.Version != workflowDevelopmentPublishJournalVersion {
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
func recoverWorkflowDevelopmentPublishTransaction(workspace string) error {
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
