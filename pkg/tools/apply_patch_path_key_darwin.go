//go:build darwin

package tools

import (
	"path/filepath"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Default macOS volumes are case-insensitive. Conservatively folding on every
// Darwin volume may reject a safe pair on a case-sensitive volume, but it never
// permits two planned file roles to collapse onto one case-insensitive name.
func applyPatchPathKey(path string) string {
	return norm.NFD.String(cases.Fold().String(filepath.Clean(path)))
}
