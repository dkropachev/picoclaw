//go:build !windows

package agent

import "path/filepath"

func agentFileMutationWorkspaceKey(path string) string {
	return filepath.Clean(path)
}
