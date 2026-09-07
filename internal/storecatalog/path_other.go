//go:build !darwin && !windows

package storecatalog

import "path/filepath"

func validateCatalogPlatformPath(string) error { return nil }

func catalogPathKey(path string) string { return filepath.Clean(path) }

func sameCatalogResolvedPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}
