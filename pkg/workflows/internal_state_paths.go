package workflows

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkflowInternalStateRootUnsafe reports that a workflow-owned state path
// would follow a symlink outside its workspace. Callers must fail closed
// before reading, locking, writing, or removing anything below that root.
var ErrWorkflowInternalStateRootUnsafe = errors.New(
	"workflow internal state root is unsafe",
)

func resolveWorkflowInternalPath(
	workspace string,
	rootName string,
	parts ...string,
) (string, error) {
	root, err := resolveWorkflowInternalRoot(workspace, rootName)
	if err != nil {
		return "", err
	}
	target := filepath.Join(append([]string{root}, parts...)...)
	targetEval, err := evalWorkflowPathPrefix(target)
	if err != nil {
		return "", fmt.Errorf(
			"%w: resolve %s path: %v",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
			err,
		)
	}
	if !workflowPathStrictlyInside(root, targetEval) {
		return "", fmt.Errorf(
			"%w: %s path escapes its workspace root",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
		)
	}
	return target, nil
}

func resolveWorkflowInternalRoot(workspace, rootName string) (string, error) {
	if !isWorkflowInternalStateRoot(rootName) {
		return "", fmt.Errorf(
			"%w: unsupported root %q",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
		)
	}
	workspacePath := strings.TrimSpace(workspace)
	if workspacePath == "" {
		workspacePath = "."
	}
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf(
			"%w: resolve workflow workspace: %v",
			ErrWorkflowInternalStateRootUnsafe,
			err,
		)
	}
	workspaceEval, err := evalWorkflowPathPrefix(workspaceAbs)
	if err != nil {
		return "", fmt.Errorf(
			"%w: resolve workflow workspace symlink: %v",
			ErrWorkflowInternalStateRootUnsafe,
			err,
		)
	}

	rootPath := filepath.Join(workspaceAbs, rootName)
	if info, lstatErr := os.Lstat(rootPath); lstatErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"%w: %s must not be a symlink",
				ErrWorkflowInternalStateRootUnsafe,
				rootName,
			)
		}
	} else if !os.IsNotExist(lstatErr) {
		return "", fmt.Errorf(
			"%w: inspect %s: %v",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
			lstatErr,
		)
	}

	rootEval, err := evalWorkflowPathPrefix(rootPath)
	if err != nil {
		return "", fmt.Errorf(
			"%w: resolve %s symlink: %v",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
			err,
		)
	}
	if !workflowPathStrictlyInside(workspaceEval, rootEval) {
		return "", fmt.Errorf(
			"%w: %s escapes workflow workspace",
			ErrWorkflowInternalStateRootUnsafe,
			rootName,
		)
	}
	return rootEval, nil
}

func isWorkflowInternalStateRoot(rootName string) bool {
	switch rootName {
	case "workflow_state", "workflow_validations", "workflow_dev":
		return true
	default:
		return false
	}
}
