package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	workflowTemplateInstallJournalVersion = 2
	workflowTemplateInstallJournalFile    = "template-transaction.json"

	workflowTemplateInstallPhasePrepared  = "prepared"
	workflowTemplateInstallPhaseCommitted = "committed"

	workflowTemplateInstallStagePrepared             = "prepared"
	workflowTemplateInstallStageTargetWriteStarted   = "target_write_started"
	workflowTemplateInstallStageManifestWriteStarted = "manifest_write_started"
	workflowTemplateInstallStageCommitted            = "committed"
)

var ErrWorkflowTemplateRecoveryFailed = errors.New(
	"workflow template installation recovery failed",
)

type workflowTemplateInstallFileSnapshot struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type workflowTemplateInstallFileTransition struct {
	Preimage  workflowTemplateInstallFileSnapshot `json:"preimage"`
	Postimage workflowTemplateInstallFileSnapshot `json:"postimage"`
}

type workflowTemplateInstallJournal struct {
	Version        int                                   `json:"version"`
	Phase          string                                `json:"phase"`
	Stage          string                                `json:"stage"`
	DefinitionsDir string                                `json:"definitions_dir"`
	TemplateName   string                                `json:"template_name"`
	TargetRef      string                                `json:"target_ref"`
	Target         workflowTemplateInstallFileTransition `json:"target"`
	Manifest       workflowTemplateInstallFileTransition `json:"manifest"`
}

type workflowTemplateInstallBoundary string

const (
	workflowTemplateInstallBoundaryPrepared            workflowTemplateInstallBoundary = "prepared"
	workflowTemplateInstallBoundaryTargetWritten       workflowTemplateInstallBoundary = "target_written"
	workflowTemplateInstallBoundaryManifestRevalidated workflowTemplateInstallBoundary = "manifest_revalidated"
	workflowTemplateInstallBoundaryCommitted           workflowTemplateInstallBoundary = "committed"
)

// workflowTemplateInstallHooks exists only to make durable transaction
// boundaries deterministic in tests. Production callers always pass nil.
type workflowTemplateInstallHooks struct {
	afterBoundary       func(workflowTemplateInstallBoundary) error
	leaveJournalOnError bool
	writeJournal        workflowTemplateInstallJournalWriter
}

type workflowTemplateInstallJournalWriter func(
	string,
	*workflowTemplateInstallJournal,
) error

func workflowTemplateInstallSnapshot(
	snapshot workflowTemplateFileSnapshot,
) workflowTemplateInstallFileSnapshot {
	return workflowTemplateInstallFileSnapshot{
		Exists: snapshot.exists,
		Data:   snapshot.data,
		Mode:   uint32(snapshot.mode.Perm()),
	}
}

func workflowTemplateInstallTransition(
	preimage workflowTemplateFileSnapshot,
	postimage workflowTemplateFileSnapshot,
) workflowTemplateInstallFileTransition {
	return workflowTemplateInstallFileTransition{
		Preimage:  workflowTemplateInstallSnapshot(preimage),
		Postimage: workflowTemplateInstallSnapshot(postimage),
	}
}

func (snapshot workflowTemplateInstallFileSnapshot) fileSnapshot(
	path string,
) workflowTemplateFileSnapshot {
	return workflowTemplateFileSnapshot{
		path:   path,
		exists: snapshot.Exists,
		data:   snapshot.Data,
		mode:   fs.FileMode(snapshot.Mode).Perm(),
	}
}

func workflowTemplateInstallJournalPath(workspace string) string {
	return filepath.Join(
		workspace,
		workflowMutationStateDir,
		workflowTemplateInstallJournalFile,
	)
}

func writeWorkflowTemplateInstallJournal(
	workspace string,
	journal *workflowTemplateInstallJournal,
) error {
	if err := validateWorkflowTemplateInstallJournal(journal); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	path, err := checkedWorkflowTemplateInstallJournalPath(workspace)
	if err != nil {
		return err
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeWorkflowTemplateAtomic(path, data, 0o600)
}

func writeWorkflowTemplateInstallCommitWithRetry(
	workspace string,
	journal *workflowTemplateInstallJournal,
	writeJournal workflowTemplateInstallJournalWriter,
) error {
	if writeJournal == nil {
		writeJournal = writeWorkflowTemplateInstallJournal
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

func readWorkflowTemplateInstallJournal(
	workspace string,
) (*workflowTemplateInstallJournal, bool, error) {
	path, err := checkedWorkflowTemplateInstallJournalPath(workspace)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	var journal workflowTemplateInstallJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, false, err
	}
	if err := validateWorkflowTemplateInstallJournal(&journal); err != nil {
		return nil, false, err
	}
	return &journal, false, nil
}

func validateWorkflowTemplateInstallJournal(
	journal *workflowTemplateInstallJournal,
) error {
	if journal == nil {
		return fmt.Errorf("workflow template install journal is required")
	}
	if journal.Version != workflowTemplateInstallJournalVersion {
		return fmt.Errorf("unsupported workflow template install journal version")
	}
	switch journal.Phase {
	case workflowTemplateInstallPhasePrepared, workflowTemplateInstallPhaseCommitted:
	default:
		return fmt.Errorf("invalid workflow template install journal phase")
	}
	switch journal.Stage {
	case workflowTemplateInstallStagePrepared,
		workflowTemplateInstallStageTargetWriteStarted,
		workflowTemplateInstallStageManifestWriteStarted:
		if journal.Phase != workflowTemplateInstallPhasePrepared {
			return fmt.Errorf("invalid workflow template install journal stage")
		}
	case workflowTemplateInstallStageCommitted:
		if journal.Phase != workflowTemplateInstallPhaseCommitted {
			return fmt.Errorf("invalid workflow template install journal stage")
		}
	default:
		return fmt.Errorf("invalid workflow template install journal stage")
	}
	template, ok := findBuiltInWorkflowTemplate(journal.TemplateName)
	if !ok || template.name != journal.TemplateName || template.ref != journal.TargetRef {
		return fmt.Errorf("invalid workflow template install journal template")
	}
	canonical, err := CanonicalLocalRef(journal.TargetRef)
	if err != nil || canonical != journal.TargetRef {
		return fmt.Errorf("invalid workflow template install journal target")
	}
	cleanDir, err := cleanDefinitionsDir(journal.DefinitionsDir)
	if err != nil || filepath.ToSlash(cleanDir) != journal.DefinitionsDir {
		return fmt.Errorf("invalid workflow template install journal definitions dir")
	}
	for _, snapshot := range []workflowTemplateInstallFileSnapshot{
		journal.Target.Preimage,
		journal.Target.Postimage,
		journal.Manifest.Preimage,
		journal.Manifest.Postimage,
	} {
		if err := validateWorkflowTemplateInstallFileSnapshot(snapshot); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowTemplateInstallFileSnapshot(
	snapshot workflowTemplateInstallFileSnapshot,
) error {
	if snapshot.Mode&^uint32(0o777) != 0 {
		return fmt.Errorf("invalid workflow template install journal file mode")
	}
	if !snapshot.Exists && (len(snapshot.Data) != 0 || snapshot.Mode != 0) {
		return fmt.Errorf("invalid workflow template install journal file mode")
	}
	return nil
}

// recoverWorkflowTemplateInstallTransaction runs only while the caller owns
// the workspace mutation lock. Prepared installs restore exact preimages;
// committed installs only need their journal finalized.
func recoverWorkflowTemplateInstallTransaction(workspace string) error {
	journal, missing, err := readWorkflowTemplateInstallJournal(workspace)
	if err != nil {
		return errors.Join(ErrWorkflowTemplateRecoveryFailed, err)
	}
	if missing {
		return nil
	}
	if journal.Phase == workflowTemplateInstallPhaseCommitted {
		if err := removeWorkflowTemplateInstallJournal(workspace); err != nil {
			return errors.Join(ErrWorkflowTemplateRecoveryFailed, err)
		}
		return nil
	}

	resolved, err := (Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: journal.DefinitionsDir,
	}).ResolveLocal(journal.TargetRef)
	if err != nil {
		return errors.Join(ErrWorkflowTemplateRecoveryFailed, err)
	}
	manifestPath, err := checkedCompatibilityManifestPath(workspace)
	if err != nil {
		return errors.Join(ErrWorkflowTemplateRecoveryFailed, err)
	}
	transitions := []workflowFileRecoveryTransition{
		{
			label:     "compatibility manifest",
			path:      manifestPath,
			preimage:  journal.Manifest.Preimage.fileSnapshot(manifestPath),
			postimage: journal.Manifest.Postimage.fileSnapshot(manifestPath),
		},
		{
			label:     "workflow template target",
			path:      resolved.Path,
			preimage:  journal.Target.Preimage.fileSnapshot(resolved.Path),
			postimage: journal.Target.Postimage.fileSnapshot(resolved.Path),
		},
	}
	if err := recoverWorkflowFileTransitions(transitions...); err != nil {
		return errors.Join(
			ErrWorkflowTemplateRecoveryFailed,
			err,
		)
	}
	if err := removeWorkflowTemplateInstallJournal(workspace); err != nil {
		return errors.Join(ErrWorkflowTemplateRecoveryFailed, err)
	}
	return nil
}

func removeWorkflowTemplateInstallJournal(workspace string) error {
	path, err := checkedWorkflowTemplateInstallJournalPath(workspace)
	if err != nil {
		return err
	}
	if err := fileutil.RemoveDurable(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func checkedWorkflowTemplateInstallJournalPath(workspace string) (string, error) {
	return resolveWorkflowInternalPath(
		workspace,
		workflowMutationStateDir,
		workflowTemplateInstallJournalFile,
	)
}

func workflowTemplateInstallJournalIsCommitted(workspace string) (bool, error) {
	journal, missing, err := readWorkflowTemplateInstallJournal(workspace)
	if err != nil || missing {
		return false, err
	}
	return journal.Phase == workflowTemplateInstallPhaseCommitted, nil
}
