//go:build windows

package isolation

import (
	"path/filepath"
	"strings"
)

func normalizeExecutableSearchPath(value string) string {
	seen := make(map[string]struct{})
	entries := make([]string, 0)
	for _, entry := range filepath.SplitList(value) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		entry = filepath.Clean(entry)
		canonical := strings.ToUpper(entry)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		entries = append(entries, entry)
	}
	return strings.Join(entries, string(filepath.ListSeparator))
}

func normalizeExecutablePathExtensions(value string, present bool) (string, bool) {
	if !present {
		return "", false
	}
	if value == "" {
		return "", true
	}
	return strings.Join(windowsConfiguredExecutableExtensions(value), ";"), true
}
