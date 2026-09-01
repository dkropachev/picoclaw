//go:build darwin

package fstools

import (
	"path/filepath"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Default macOS volumes are case-insensitive. Folding every Darwin path is
// conservative on case-sensitive volumes and prevents a false authorization.
func fileMutationPathKey(path string) string {
	return norm.NFD.String(cases.Fold().String(filepath.Clean(path)))
}

// Distinct-path keys must preserve case and normalization on case-sensitive
// Darwin volumes. Conservative folding is correct for containment denial but
// unsafe for dedupe: it can erase a separate protected inode.
func fileMutationDistinctPathKey(path string) string {
	return filepath.Clean(path)
}
