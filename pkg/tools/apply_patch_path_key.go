//go:build !windows && !darwin

package tools

import "path/filepath"

func applyPatchPathKey(path string) string {
	return filepath.Clean(path)
}
