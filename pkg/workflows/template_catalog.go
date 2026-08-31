package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type WorkflowTemplateState string

const (
	WorkflowTemplateStateAvailable WorkflowTemplateState = "available"
	WorkflowTemplateStateInstalled WorkflowTemplateState = "installed"
	WorkflowTemplateStateModified  WorkflowTemplateState = "modified"
	WorkflowTemplateStateBlocked   WorkflowTemplateState = "blocked"
)

type WorkflowTemplateBlockedReason string

const (
	WorkflowTemplateBlockedConfiguration WorkflowTemplateBlockedReason = "configuration_invalid"
	WorkflowTemplateBlockedNotRegular    WorkflowTemplateBlockedReason = "target_not_regular"
	WorkflowTemplateBlockedUnavailable   WorkflowTemplateBlockedReason = "target_unavailable"
)

var (
	ErrWorkflowTemplateUnknown = errors.New("workflow template is unknown")

	ErrWorkflowTemplateOverwriteRequired = errors.New(
		"workflow template was modified; explicit overwrite is required",
	)
	ErrWorkflowTemplateTargetBlocked = errors.New(
		"workflow template target is blocked",
	)
	ErrWorkflowTemplateCatalogUnavailable = errors.New(
		"workflow template catalog is unavailable",
	)
	ErrWorkflowTemplateInstallFailed = errors.New(
		"workflow template installation failed",
	)
	ErrWorkflowTemplateRevalidationFailed = errors.New(
		"workflow template compatibility revalidation failed",
	)
	ErrWorkflowTemplateRollbackFailed = errors.New(
		"workflow template installation rollback failed",
	)
)

// WorkflowTemplateCatalogEntry is the safe, path-free view of one built-in
// workflow template.
type WorkflowTemplateCatalogEntry struct {
	Name          string                        `json:"name"`
	Ref           string                        `json:"ref"`
	State         WorkflowTemplateState         `json:"state"`
	BlockedReason WorkflowTemplateBlockedReason `json:"blocked_reason,omitempty"`
}

// WorkflowTemplateInstallResult is the safe, path-free outcome returned by
// the transactional catalog installer.
type WorkflowTemplateInstallResult struct {
	Name        string                `json:"name"`
	Ref         string                `json:"ref"`
	State       WorkflowTemplateState `json:"state"`
	Installed   bool                  `json:"installed"`
	Overwritten bool                  `json:"overwritten,omitempty"`
	Revalidated bool                  `json:"revalidated"`
}

type workflowTemplateTarget struct {
	entry WorkflowTemplateCatalogEntry
	path  string
}

type workflowTemplateFileSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   fs.FileMode
}

type workflowTemplateRevalidateFunc func(
	context.Context,
	string,
	RuntimeCompatibility,
	*workflowCompatibilityOverlay,
	...LocalOption,
) (*WorkflowCompatibilityManifest, error)

// ListWorkflowTemplates returns the built-in template catalog without
// exposing resolved filesystem paths or raw filesystem errors.
func ListWorkflowTemplates(
	ctx context.Context,
	workspace string,
	opts ...LocalOption,
) ([]WorkflowTemplateCatalogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, errors.Join(ErrWorkflowTemplateCatalogUnavailable, err)
	}
	defer unlock()

	entries := make([]WorkflowTemplateCatalogEntry, 0, len(builtInWorkflowTemplateRegistry))
	for _, template := range builtInWorkflowTemplateRegistry {
		target := inspectWorkflowTemplateTarget(workspace, template, opts...)
		entries = append(entries, target.entry)
	}
	return entries, nil
}

// InstallWorkflowTemplateWithCompatibility installs one built-in template and
// rebuilds the compatibility manifest in the same mutation transaction. A
// failed revalidation restores both the target definition and the manifest.
func InstallWorkflowTemplateWithCompatibility(
	ctx context.Context,
	workspace string,
	name string,
	overwrite bool,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*WorkflowTemplateInstallResult, error) {
	return installWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		name,
		overwrite,
		runtime,
		buildCompatibilityManifestLocked,
		opts...,
	)
}

func installWorkflowTemplateWithCompatibility(
	ctx context.Context,
	workspace string,
	name string,
	overwrite bool,
	runtime RuntimeCompatibility,
	revalidate workflowTemplateRevalidateFunc,
	opts ...LocalOption,
) (*WorkflowTemplateInstallResult, error) {
	return installWorkflowTemplateTransaction(
		ctx,
		workspace,
		name,
		overwrite,
		runtime,
		revalidate,
		nil,
		opts...,
	)
}

func installWorkflowTemplateTransaction(
	ctx context.Context,
	workspace string,
	name string,
	overwrite bool,
	runtime RuntimeCompatibility,
	revalidate workflowTemplateRevalidateFunc,
	hooks *workflowTemplateInstallHooks,
	opts ...LocalOption,
) (*WorkflowTemplateInstallResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	template, ok := findBuiltInWorkflowTemplate(name)
	if !ok {
		return nil, ErrWorkflowTemplateUnknown
	}
	if revalidate == nil {
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, errors.Join(ErrWorkflowTemplateInstallFailed, err)
	}
	defer unlock()
	active, err := getWorkflowDevelopmentSessionLocked(workspace)
	if err != nil {
		return nil, ErrWorkflowTemplateInstallFailed
	}
	if active != nil {
		return nil, ErrActiveDevelopmentExists
	}

	target := inspectWorkflowTemplateTarget(workspace, template, opts...)
	switch target.entry.State {
	case WorkflowTemplateStateBlocked:
		return nil, ErrWorkflowTemplateTargetBlocked
	case WorkflowTemplateStateModified:
		if !overwrite {
			return nil, ErrWorkflowTemplateOverwriteRequired
		}
	case WorkflowTemplateStateAvailable, WorkflowTemplateStateInstalled:
	default:
		return nil, ErrWorkflowTemplateInstallFailed
	}

	targetSnapshot, err := captureWorkflowTemplateFile(target.path)
	if err != nil {
		return nil, ErrWorkflowTemplateTargetBlocked
	}
	previousManifest, manifestMissing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	manifestSnapshot := workflowTemplateFileSnapshot{}
	if !manifestMissing {
		manifestSnapshot.exists = true
		manifestSnapshot.mode = 0o600
		manifestSnapshot.data, err = json.Marshal(previousManifest)
		if err != nil {
			return nil, ErrWorkflowTemplateRevalidationFailed
		}
	}
	local := collectLocalOptions(opts...)
	definitionsDir, err := cleanDefinitionsDir(local.DefinitionsDir)
	if err != nil {
		return nil, ErrWorkflowTemplateInstallFailed
	}
	manifest, revalidateErr := revalidate(
		ctx,
		workspace,
		runtime,
		&workflowCompatibilityOverlay{
			ref:  template.ref,
			data: []byte(template.raw),
		},
		opts...,
	)
	if revalidateErr == nil &&
		!templateHasValidCompatibilityStamp(manifest, template.ref) {
		revalidateErr = ErrWorkflowTemplateRevalidationFailed
	}
	if revalidateErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	targetPostimage := workflowTemplateFileSnapshot{
		path:   target.path,
		exists: true,
		data:   []byte(template.raw),
		mode:   0o644,
	}
	if targetSnapshot.exists &&
		bytes.Equal(targetSnapshot.data, targetPostimage.data) {
		// The idempotent install path deliberately preserves the existing
		// regular file mode because it does not rewrite the target.
		targetPostimage.mode = targetSnapshot.mode
	}
	manifestPostimage := workflowTemplateFileSnapshot{
		exists: true,
		data:   manifestData,
		mode:   0o600,
	}
	journal := &workflowTemplateInstallJournal{
		Version:        workflowTemplateInstallJournalVersion,
		Phase:          workflowTemplateInstallPhasePrepared,
		Stage:          workflowTemplateInstallStagePrepared,
		DefinitionsDir: filepath.ToSlash(definitionsDir),
		TemplateName:   template.name,
		TargetRef:      template.ref,
		Target: workflowTemplateInstallTransition(
			targetSnapshot,
			targetPostimage,
		),
		Manifest: workflowTemplateInstallTransition(
			manifestSnapshot,
			manifestPostimage,
		),
	}
	writeJournal := workflowTemplateInstallJournalWriter(
		writeWorkflowTemplateInstallJournal,
	)
	if hooks != nil && hooks.writeJournal != nil {
		writeJournal = hooks.writeJournal
	}
	if journalErr := writeJournal(workspace, journal); journalErr != nil {
		if recoveryErr := recoverWorkflowTemplateInstallTransaction(workspace); recoveryErr != nil {
			return nil, errors.Join(
				ErrWorkflowTemplateInstallFailed,
				journalErr,
				recoveryErr,
			)
		}
		return nil, ErrWorkflowTemplateInstallFailed
	}
	fail := func(installErr error) error {
		if hooks != nil && hooks.leaveJournalOnError {
			return installErr
		}
		if rollbackErr := recoverWorkflowTemplateInstallTransaction(workspace); rollbackErr != nil {
			return errors.Join(
				installErr,
				ErrWorkflowTemplateRollbackFailed,
				rollbackErr,
			)
		}
		return installErr
	}
	checkBoundary := func(boundary workflowTemplateInstallBoundary) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if hooks != nil && hooks.afterBoundary != nil {
			return hooks.afterBoundary(boundary)
		}
		return nil
	}
	if boundaryErr := checkBoundary(workflowTemplateInstallBoundaryPrepared); boundaryErr != nil {
		return nil, fail(boundaryErr)
	}

	journal.Stage = workflowTemplateInstallStageTargetWriteStarted
	if journalErr := writeJournal(workspace, journal); journalErr != nil {
		return nil, fail(journalErr)
	}
	installed, err := installWorkflowTemplateLocked(
		ctx,
		workspace,
		template.name,
		template.ref,
		template.raw,
		overwrite,
		opts...,
	)
	if err != nil {
		err = fail(err)
		if errors.Is(err, ErrWorkflowTemplateRollbackFailed) {
			return nil, err
		}
		if errors.Is(err, ErrWorkflowTemplateOverwriteRequired) ||
			errors.Is(err, ErrWorkflowTemplateTargetBlocked) {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, ErrWorkflowTemplateInstallFailed
	}
	if err := checkBoundary(workflowTemplateInstallBoundaryTargetWritten); err != nil {
		return nil, fail(err)
	}

	journal.Stage = workflowTemplateInstallStageManifestWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		err = fail(err)
		if errors.Is(err, ErrWorkflowTemplateRollbackFailed) {
			return nil, err
		}
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	if err := writeCompatibilityManifest(workspace, manifest); err != nil {
		err = fail(err)
		if errors.Is(err, ErrWorkflowTemplateRollbackFailed) {
			return nil, err
		}
		return nil, ErrWorkflowTemplateRevalidationFailed
	}
	if err := checkBoundary(workflowTemplateInstallBoundaryManifestRevalidated); err != nil {
		return nil, fail(err)
	}

	result := &WorkflowTemplateInstallResult{
		Name:        template.name,
		Ref:         template.ref,
		State:       WorkflowTemplateStateInstalled,
		Installed:   installed.Installed,
		Overwritten: installed.Overwritten,
		Revalidated: true,
	}
	journal.Phase = workflowTemplateInstallPhaseCommitted
	journal.Stage = workflowTemplateInstallStageCommitted
	if err := writeWorkflowTemplateInstallCommitWithRetry(
		workspace,
		journal,
		writeJournal,
	); err != nil {
		// A marker can be visible after a failed directory sync without being
		// durable. Leave the ambiguous journal untouched and return an error.
		// Recovery will later finalize a durable committed marker or roll back
		// the durable prepared marker.
		return nil, errors.Join(ErrWorkflowTemplateInstallFailed, err)
	}
	if err := checkBoundary(workflowTemplateInstallBoundaryCommitted); err != nil {
		if hooks != nil && hooks.leaveJournalOnError {
			return nil, err
		}
		_ = removeWorkflowTemplateInstallJournal(workspace)
		return result, nil
	}

	// Once the committed marker is durable, cleanup is retryable housekeeping:
	// a leftover committed journal is finalized by the next locked operation.
	_ = removeWorkflowTemplateInstallJournal(workspace)
	return result, nil
}

func inspectWorkflowTemplateTarget(
	workspace string,
	template builtInWorkflowTemplate,
	opts ...LocalOption,
) workflowTemplateTarget {
	entry := WorkflowTemplateCatalogEntry{
		Name:  template.name,
		Ref:   template.ref,
		State: WorkflowTemplateStateBlocked,
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(template.ref)
	if err != nil {
		entry.BlockedReason = WorkflowTemplateBlockedConfiguration
		return workflowTemplateTarget{entry: entry}
	}
	target := workflowTemplateTarget{entry: entry, path: resolved.Path}
	info, err := os.Lstat(resolved.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		target.entry.State = WorkflowTemplateStateAvailable
		target.entry.BlockedReason = ""
		return target
	case err != nil:
		target.entry.BlockedReason = WorkflowTemplateBlockedUnavailable
		return target
	case !info.Mode().IsRegular():
		target.entry.BlockedReason = WorkflowTemplateBlockedNotRegular
		return target
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		target.entry.BlockedReason = WorkflowTemplateBlockedUnavailable
		return target
	}
	if bytes.Equal(data, []byte(template.raw)) {
		target.entry.State = WorkflowTemplateStateInstalled
	} else {
		target.entry.State = WorkflowTemplateStateModified
	}
	target.entry.BlockedReason = ""
	return target
}

func captureWorkflowTemplateFile(path string) (workflowTemplateFileSnapshot, error) {
	snapshot := workflowTemplateFileSnapshot{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return workflowTemplateFileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return workflowTemplateFileSnapshot{}, ErrWorkflowTemplateTargetBlocked
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return workflowTemplateFileSnapshot{}, err
	}
	snapshot.exists = true
	snapshot.data = data
	snapshot.mode = info.Mode().Perm()
	return snapshot, nil
}

func restoreWorkflowTemplateFile(snapshot workflowTemplateFileSnapshot) error {
	if !snapshot.exists {
		if err := fileutil.RemoveDurable(snapshot.path); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(snapshot.path), 0o755); err != nil {
		return err
	}
	return writeWorkflowTemplateAtomic(snapshot.path, snapshot.data, snapshot.mode)
}

func writeWorkflowTemplateAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	return writeWorkflowTemplateAtomicWithHooks(
		path,
		data,
		mode,
		replaceWorkflowFile,
		syncWorkflowRunDirectory,
	)
}

func writeWorkflowTemplateAtomicWithHooks(
	path string,
	data []byte,
	mode fs.FileMode,
	replace func(string, string) error,
	syncDir func(string) error,
) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replace(tmpPath, path); err != nil {
		return err
	}
	// A successful replacement transfers ownership away from the source name.
	// Do not remove that pathname if the following directory sync fails: another
	// process may already have created a new file there.
	cleanup = false
	return syncDir(dir)
}

func templateHasValidCompatibilityStamp(
	manifest *WorkflowCompatibilityManifest,
	ref string,
) bool {
	if manifest == nil {
		return false
	}
	stamp, ok := manifest.Workflows[ref]
	return ok && (stamp.Status == WorkflowValidationStatusValid ||
		stamp.Status == WorkflowValidationStatusNeedsReview)
}
