//go:build !windows && !darwin

package fstools

import "path/filepath"

func fileMutationPathKey(path string) string {
	return filepath.Clean(path)
}
