//go:build featuretools

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var requiredFeatureHeadings = []string{
	"## Feature ID",
	"## Behavior Summary",
	"## Reconstruction Notes",
	"## Requirements",
	"## Data And State Model",
	"## Auxiliary Interfaces",
	"## Algorithms And Ordering",
	"## Cross-Feature Behavior",
	"## Failure And Edge Cases",
	"## Acceptance Evidence",
	"## Implementation Anchors",
	"## Surface Ownership",
}

func main() {
	root, err := repoRoot()
	if err != nil {
		fail("feature lint: %v", err)
	}
	if err := lintFeatures(root); err != nil {
		fail("feature lint: %v", err)
	}
	fmt.Println("feature lint: ok")
}

func lintFeatures(root string) error {
	featuresDir := filepath.Join(root, "docs", "features")
	readmePath := filepath.Join(featuresDir, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("read docs/features/README.md: %w", err)
	}

	specs, err := featureSpecFiles(featuresDir)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("no feature specs under docs/features")
	}

	var failures []string
	ids := make(map[string]string)
	var ownerPatterns []string
	for _, spec := range specs {
		relPath := rel(root, spec)
		if !strings.Contains(string(readme), filepath.Base(spec)) {
			failures = append(failures, fmt.Sprintf("%s: missing from docs/features/README.md", relPath))
		}
		data, err := os.ReadFile(spec)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: read: %v", relPath, err))
			continue
		}
		text := string(data)
		for _, heading := range requiredFeatureHeadings {
			if !strings.Contains(text, heading+"\n") && !strings.HasSuffix(text, heading) {
				failures = append(failures, fmt.Sprintf("%s: missing heading %q", relPath, heading))
			}
		}
		if strings.Contains(text, "Test gap:") && os.Getenv("ALLOW_FEATURE_TEST_GAPS") != "1" {
			failures = append(failures, fmt.Sprintf("%s: contains forbidden Test gap entry", relPath))
		}
		specIDs := requirementIDs(text)
		if len(specIDs) == 0 {
			failures = append(failures, fmt.Sprintf("%s: no requirement IDs found", relPath))
		}
		evidence := section(text, "## Acceptance Evidence")
		for _, id := range specIDs {
			if previous, ok := ids[id]; ok {
				failures = append(failures, fmt.Sprintf("%s: duplicate requirement ID %s, first seen in %s", relPath, id, previous))
			} else {
				ids[id] = relPath
			}
			if !strings.Contains(evidence, id) {
				failures = append(failures, fmt.Sprintf("%s: %s missing from Acceptance Evidence", relPath, id))
			}
		}
		ownerPatterns = append(ownerPatterns, ownershipPatterns(text)...)
		failures = append(failures, validateMarkdownLinks(root, spec, text)...)
	}

	if len(ownerPatterns) == 0 {
		failures = append(failures, "docs/features: no Owns: surface ownership patterns found")
	}

	surfaces, err := discoverFeatureSurfaces(root)
	if err != nil {
		failures = append(failures, fmt.Sprintf("discover surfaces: %v", err))
	} else {
		for _, surface := range surfaces {
			if !surfaceOwned(surface.ID, ownerPatterns) {
				failures = append(failures, fmt.Sprintf("unowned surface: %s (%s)", surface.ID, surface.Source))
			}
		}
	}

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("%d failure(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return nil
}

func featureSpecFiles(featuresDir string) ([]string, error) {
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		return nil, fmt.Errorf("read docs/features: %w", err)
	}
	var specs []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		switch entry.Name() {
		case "README.md", "template.md":
			continue
		}
		specs = append(specs, filepath.Join(featuresDir, entry.Name()))
	}
	sort.Strings(specs)
	return specs, nil
}

func requirementIDs(text string) []string {
	re := regexp.MustCompile(`FR-[A-Z0-9-]+-[0-9]{3}`)
	seen := make(map[string]bool)
	var ids []string
	for _, id := range re.FindAllString(text, -1) {
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func section(text, heading string) string {
	idx := strings.Index(text, heading)
	if idx < 0 {
		return ""
	}
	tail := text[idx+len(heading):]
	next := regexp.MustCompile(`(?m)^## `).FindStringIndex(tail)
	if next == nil {
		return tail
	}
	return tail[:next[0]]
}

func ownershipPatterns(text string) []string {
	re := regexp.MustCompile(`(?m)^Owns:\s+(.+)$`)
	var patterns []string
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		line := strings.TrimSpace(match[1])
		line = strings.Trim(line, "`")
		if line != "" {
			patterns = append(patterns, line)
		}
	}
	return patterns
}

func surfaceOwned(surface string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == surface || globMatch(pattern, surface) {
			return true
		}
	}
	return false
}

func globMatch(pattern, value string) bool {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	quoted = strings.ReplaceAll(quoted, `\?`, `.`)
	re, err := regexp.Compile("^" + quoted + "$")
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func validateMarkdownLinks(root, spec, text string) []string {
	re := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	var failures []string
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		target := strings.TrimSpace(match[1])
		if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		if strings.Contains(target, " ") {
			target = strings.Fields(target)[0]
		}
		if hash := strings.IndexByte(target, '#'); hash >= 0 {
			target = target[:hash]
		}
		if target == "" {
			continue
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(spec), filepath.FromSlash(target)))
		if relPath, err := filepath.Rel(root, resolved); err != nil || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
			failures = append(failures, fmt.Sprintf("%s: link escapes repository: %s", rel(root, spec), target))
			continue
		}
		if _, err := os.Stat(resolved); err != nil {
			failures = append(failures, fmt.Sprintf("%s: broken link %s", rel(root, spec), target))
		}
	}
	return failures
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
