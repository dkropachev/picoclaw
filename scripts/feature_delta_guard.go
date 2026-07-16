//go:build featuretools

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	base := flag.String("base", defaultBaseRef(), "base git ref to compare")
	head := flag.String("head", "HEAD", "head git ref to compare")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail("feature delta: %v", err)
	}
	if err := runFeatureDeltaGuard(root, *base, *head); err != nil {
		fail("feature delta: %v", err)
	}
	fmt.Println("feature delta: ok")
}

func runFeatureDeltaGuard(root, base, head string) error {
	specs, err := loadFeatureSpecs(root)
	if err != nil {
		return err
	}
	frontendOwnership, err := loadFrontendOwnershipConfig(root)
	if err != nil {
		return err
	}
	changed, err := changedFiles(root, base, head)
	if err != nil {
		return err
	}

	changedSpecs := make(map[string]bool)
	var changedProd []string
	for _, path := range changed {
		if isFeatureSpecPath(path) {
			changedSpecs[normalizeRepoPath(path)] = true
		}
		if isProductionCodePath(path) {
			changedProd = append(changedProd, normalizeRepoPath(path))
		}
	}

	var failures []string
	for _, path := range changedProd {
		owners := codeOwnersForPath(specs, path)
		if len(owners) == 0 {
			failures = append(failures, fmt.Sprintf("%s: production code changed but no docs/features spec declares Owns: CODE for it", path))
			continue
		}
		if expectedSpecs := frontendExpectedSpecPaths(frontendOwnership, path); len(expectedSpecs) > 0 {
			if !expectedOwnerSpecChanged(expectedSpecs, owners, changedSpecs) {
				failures = append(failures, fmt.Sprintf("%s: frontend code changed; update the expected owning feature spec: %s", path, strings.Join(expectedSpecs, ", ")))
				continue
			}
			continue
		}
		if ownerSpecChanged(owners, changedSpecs) {
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: production code changed; update one owning feature spec: %s", path, ownerSpecList(owners)))
	}

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("%d failure(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return nil
}

func ownerSpecChanged(owners []featureOwnership, changedSpecs map[string]bool) bool {
	for _, owner := range owners {
		if changedSpecs[owner.SpecRelPath] {
			return true
		}
	}
	return false
}

func expectedOwnerSpecChanged(expectedSpecs []string, owners []featureOwnership, changedSpecs map[string]bool) bool {
	expected := make(map[string]bool)
	for _, spec := range expectedSpecs {
		expected[spec] = true
	}
	for _, owner := range owners {
		if expected[owner.SpecRelPath] && changedSpecs[owner.SpecRelPath] {
			return true
		}
	}
	return false
}

func ownerSpecList(owners []featureOwnership) string {
	seen := make(map[string]bool)
	var specs []string
	for _, owner := range owners {
		if seen[owner.SpecRelPath] {
			continue
		}
		seen[owner.SpecRelPath] = true
		specs = append(specs, owner.SpecRelPath)
	}
	sort.Strings(specs)
	return strings.Join(specs, ", ")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
