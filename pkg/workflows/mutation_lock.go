package workflows

import (
	"fmt"
	"path/filepath"
	"sync"
)

const workflowMutationStateDir = "workflow_state"

var workflowMutationLocks sync.Map

// WithWorkflowMutationLock runs operation while holding the same process and
// advisory workspace lock used by development, template, compatibility, and
// publish mutations. It is intended for config-coupled operations that must
// keep the active-development root stable through an external atomic save.
func WithWorkflowMutationLock(
	workspace string,
	operation func() error,
) error {
	if operation == nil {
		return fmt.Errorf("workflow mutation operation is required")
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return err
	}
	defer unlock()
	return operation()
}

// WithWorkflowMutationLockAndDevelopmentSession runs operation with a
// recovery-consistent active-development snapshot while holding the workspace
// mutation lock. Callers outside this package must use this form when the
// active-session decision and their mutation need one indivisible boundary;
// calling GetWorkflowDevelopmentSession from a WithWorkflowMutationLock
// callback would recursively acquire the non-reentrant lock.
func WithWorkflowMutationLockAndDevelopmentSession(
	workspace string,
	operation func(*WorkflowDevelopmentSession) error,
) error {
	if operation == nil {
		return fmt.Errorf("workflow mutation operation is required")
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return err
	}
	defer unlock()
	session, err := getWorkflowDevelopmentSessionLocked(workspace)
	if err != nil {
		return err
	}
	return operation(session)
}

// lockWorkflowMutation serializes workflow definition, compatibility
// manifest, template, and development-session mutations for one workspace.
// The process mutex prevents goroutines from racing while the advisory file
// lock extends the same boundary to launcher and gateway processes.
func lockWorkflowMutation(workspace string) (func(), error) {
	key, err := canonicalWorkflowWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	actual, _ := workflowMutationLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := actual.(*sync.Mutex)
	mutex.Lock()

	lockPath, err := resolveWorkflowInternalPath(
		key,
		workflowMutationStateDir,
		"mutation.lock",
	)
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	unlockFile, err := lockWorkflowMutationFile(lockPath)
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	if err := recoverWorkflowDevelopmentPublishTransaction(key); err != nil {
		unlockFile()
		mutex.Unlock()
		return nil, err
	}
	if err := recoverWorkflowTemplateInstallTransaction(key); err != nil {
		unlockFile()
		mutex.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		mutex.Unlock()
	}, nil
}

func canonicalWorkflowWorkspace(workspace string) (string, error) {
	if workspace == "" {
		workspace = "."
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workflow workspace: %w", err)
	}
	evaluated, evaluateErr := evalWorkflowPathPrefix(absolute)
	if evaluateErr != nil {
		return "", fmt.Errorf("resolve workflow workspace symlink: %w", evaluateErr)
	}
	return filepath.Clean(evaluated), nil
}
