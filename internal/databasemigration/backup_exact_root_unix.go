//go:build unix

package databasemigration

import (
	"os"
	"path/filepath"
)

func openExactBackupRoot(path string) (*os.Root, error) {
	// A trailing separator makes pathname resolution require a directory before
	// open, so a concurrent FIFO/device substitution cannot block the verifier.
	return os.OpenRoot(filepath.Clean(path) + string(os.PathSeparator))
}
