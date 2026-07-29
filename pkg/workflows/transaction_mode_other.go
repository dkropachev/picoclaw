//go:build !windows

package workflows

import "io/fs"

func normalizeWorkflowTransactionFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm()
}
