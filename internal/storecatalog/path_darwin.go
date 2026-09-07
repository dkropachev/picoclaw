//go:build darwin

package storecatalog

import (
	"path/filepath"
	"strings"
)

func validateCatalogPlatformPath(string) error { return nil }

func catalogPathKey(path string) string { return strings.ToLower(filepath.Clean(path)) }

func sameCatalogResolvedPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}
