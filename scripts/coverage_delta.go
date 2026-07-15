//go:build featuretools

package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type coverageSummary struct {
	CoveredStatements int
	TotalStatements   int
}

type coverageProfile struct {
	Global coverageSummary
	Files  map[string]coverageSummary
}

type coverageBlock struct {
	File       string
	Statements int
	Covered    bool
}

type coverageChangeScope struct {
	GlobalRelevant  bool
	ImpactedFeature map[string]bool
}

const allowedCoveredStatementJitter = 10

func main() {
	base := flag.String("base", defaultBaseRef(), "base git ref to compare")
	head := flag.String("head", "HEAD", "head git ref to compare")
	tags := flag.String("tags", "goolm,stdjson", "Go build tags for coverage runs")
	packages := flag.String("packages", "./...", "space-separated Go package patterns")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail("coverage delta: %v", err)
	}
	if err := runCoverageDelta(root, *base, *head, *tags, strings.Fields(*packages)); err != nil {
		fail("coverage delta: %v", err)
	}
}

func runCoverageDelta(root, base, head, tags string, packages []string) error {
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	specs, err := loadFeatureSpecs(root)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "picoclaw-coverage-delta-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	baseProfile, err := coverageForRef(root, tmpDir, "base", base, tags, packages)
	if err != nil {
		return err
	}
	headProfile, err := coverageForRef(root, tmpDir, "head", head, tags, packages)
	if err != nil {
		return err
	}

	scope, err := coverageScope(root, base, head, specs)
	if err != nil {
		return err
	}
	var failures []string
	if scope.GlobalRelevant && coverageRegressed(baseProfile.Global, headProfile.Global) {
		failures = append(failures, fmt.Sprintf(
			"global Go statement coverage decreased: %s -> %s",
			formatCoverage(baseProfile.Global),
			formatCoverage(headProfile.Global),
		))
	}

	baseFeature := featureCoverage(specs, baseProfile)
	headFeature := featureCoverage(specs, headProfile)
	for _, spec := range specs {
		if !scope.ImpactedFeature[spec.RelPath] {
			continue
		}
		baseSummary := baseFeature[spec.RelPath]
		headSummary := headFeature[spec.RelPath]
		if headSummary.TotalStatements == 0 {
			continue
		}
		if coverageRegressed(baseSummary, headSummary) {
			failures = append(failures, fmt.Sprintf(
				"%s Go statement coverage decreased: %s -> %s",
				spec.RelPath,
				formatCoverage(baseSummary),
				formatCoverage(headSummary),
			))
		}
		if baseSummary.TotalStatements == 0 && headSummary.TotalStatements > 0 && headSummary.CoveredStatements == 0 {
			failures = append(failures, fmt.Sprintf(
				"%s owns new Go production statements but has zero covered statements",
				spec.RelPath,
			))
		}
	}

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("%d failure(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}

	fmt.Printf("coverage delta: %s; feature coverage ok\n", globalCoverageStatus(scope.GlobalRelevant, baseProfile.Global, headProfile.Global))
	return nil
}

func coverageScope(root, base, head string, specs []featureSpecMetadata) (coverageChangeScope, error) {
	changed, err := changedFiles(root, base, head)
	if err != nil {
		return coverageChangeScope{}, err
	}
	scope := coverageChangeScope{ImpactedFeature: make(map[string]bool)}
	for _, path := range changed {
		if isCoverageRelevantChange(path) {
			scope.GlobalRelevant = true
		}
		if !isGoProductionCoverageFile(path) || !isProductionCodePath(path) {
			continue
		}
		for _, owner := range codeOwnersForPath(specs, path) {
			scope.ImpactedFeature[owner.SpecRelPath] = true
		}
	}
	return scope, nil
}

func isCoverageRelevantChange(path string) bool {
	path = normalizeRepoPath(path)
	switch path {
	case "go.mod", "go.sum":
		return true
	}
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	if strings.HasPrefix(path, "cmd/") ||
		strings.HasPrefix(path, "pkg/") ||
		strings.HasPrefix(path, "web/backend/") ||
		strings.HasPrefix(path, "scripts/") ||
		strings.HasPrefix(path, "integration/") {
		return true
	}
	return false
}

func coverageForRef(root, tmpDir, label, ref, tags string, packages []string) (coverageProfile, error) {
	sha, err := resolveGitRef(root, ref)
	if err != nil {
		return coverageProfile{}, err
	}
	worktree := filepath.Join(tmpDir, label)
	if err := gitRun(root, "worktree", "add", "--detach", "--force", worktree, sha); err != nil {
		return coverageProfile{}, fmt.Errorf("create %s worktree for %s: %w", label, ref, err)
	}
	defer func() {
		_ = gitRun(root, "worktree", "remove", "--force", worktree)
	}()

	if err := runGoGenerate(worktree, label, ref); err != nil {
		return coverageProfile{}, err
	}

	profilePath := filepath.Join(tmpDir, label+".cover.out")
	args := []string{"test"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-covermode=atomic", "-coverprofile", profilePath)
	args = append(args, packages...)
	cmd := exec.Command("go", args...)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return coverageProfile{}, fmt.Errorf("go coverage for %s (%s): %w\n%s", label, ref, err, trimCommandOutput(out))
	}

	modulePath, err := modulePath(worktree)
	if err != nil {
		return coverageProfile{}, err
	}
	profile, err := parseCoverageProfile(worktree, modulePath, profilePath)
	if err != nil {
		return coverageProfile{}, fmt.Errorf("parse %s coverage profile: %w", label, err)
	}
	return profile, nil
}

func runGoGenerate(worktree, label, ref string) error {
	cmd := exec.Command("go", "generate", "./...")
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go generate for %s (%s): %w\n%s", label, ref, err, trimCommandOutput(out))
	}
	return nil
}

func resolveGitRef(root, ref string) (string, error) {
	out, err := gitOutput(root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve git ref %s: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

func gitRun(root string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("go.mod has no module line")
}

func parseCoverageProfile(root, modulePath, profilePath string) (coverageProfile, error) {
	file, err := os.Open(profilePath)
	if err != nil {
		return coverageProfile{}, err
	}
	defer file.Close()

	blocks := make(map[string]coverageBlock)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			return coverageProfile{}, fmt.Errorf("invalid coverage line %q", line)
		}
		filePath := coverageFileToRepoPath(root, modulePath, line[:colon])
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			return coverageProfile{}, fmt.Errorf("invalid coverage fields %q", line)
		}
		statements, err := strconv.Atoi(fields[1])
		if err != nil {
			return coverageProfile{}, fmt.Errorf("invalid statement count in %q: %w", line, err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return coverageProfile{}, fmt.Errorf("invalid coverage count in %q: %w", line, err)
		}
		key := filePath + ":" + fields[0]
		block := blocks[key]
		if block.File == "" {
			block = coverageBlock{File: filePath, Statements: statements}
		}
		block.Covered = block.Covered || count > 0
		blocks[key] = block
	}
	if err := scanner.Err(); err != nil {
		return coverageProfile{}, err
	}

	profile := coverageProfile{Files: make(map[string]coverageSummary)}
	for _, block := range blocks {
		fileSummary := profile.Files[block.File]
		fileSummary.TotalStatements += block.Statements
		profile.Global.TotalStatements += block.Statements
		if block.Covered {
			fileSummary.CoveredStatements += block.Statements
			profile.Global.CoveredStatements += block.Statements
		}
		profile.Files[block.File] = fileSummary
	}
	return profile, nil
}

func coverageFileToRepoPath(root, modulePath, filePath string) string {
	filePath = filepath.ToSlash(filePath)
	if modulePath != "" && strings.HasPrefix(filePath, modulePath+"/") {
		return normalizeRepoPath(strings.TrimPrefix(filePath, modulePath+"/"))
	}
	if filepath.IsAbs(filePath) {
		if relPath, err := filepath.Rel(root, filePath); err == nil {
			return normalizeRepoPath(relPath)
		}
	}
	return normalizeRepoPath(filePath)
}

func featureCoverage(specs []featureSpecMetadata, profile coverageProfile) map[string]coverageSummary {
	result := make(map[string]coverageSummary)
	for _, spec := range specs {
		var summary coverageSummary
		for file, fileSummary := range profile.Files {
			if !isGoProductionCoverageFile(file) {
				continue
			}
			if specOwnsCodeFile(spec, file) {
				summary.CoveredStatements += fileSummary.CoveredStatements
				summary.TotalStatements += fileSummary.TotalStatements
			}
		}
		result[spec.RelPath] = summary
	}
	return result
}

func specOwnsCodeFile(spec featureSpecMetadata, file string) bool {
	for _, owner := range spec.Ownerships {
		if owner.Kind == "CODE" && codePatternMatches(owner.Pattern, file) {
			return true
		}
	}
	return false
}

func isGoProductionCoverageFile(path string) bool {
	path = normalizeRepoPath(path)
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !isIgnoredProductionPath(path)
}

func coveragePercent(summary coverageSummary) float64 {
	if summary.TotalStatements == 0 {
		return 100
	}
	return float64(summary.CoveredStatements) * 100 / float64(summary.TotalStatements)
}

func coverageRegressed(base, head coverageSummary) bool {
	if coveragePercent(head) >= coveragePercent(base) {
		return false
	}
	if head.TotalStatements > base.TotalStatements {
		return true
	}
	return base.CoveredStatements-head.CoveredStatements > allowedCoveredStatementJitter
}

func globalCoverageStatus(relevant bool, base, head coverageSummary) string {
	status := fmt.Sprintf("global %s -> %s", formatCoverage(base), formatCoverage(head))
	if !relevant {
		return status + " skipped; no Go coverage-relevant changes"
	}
	if coveragePercent(head) < coveragePercent(base) && !coverageRegressed(base, head) {
		return status + fmt.Sprintf(" within %d-statement jitter band", allowedCoveredStatementJitter)
	}
	return status
}

func formatCoverage(summary coverageSummary) string {
	return fmt.Sprintf("%.2f%% (%d/%d)", coveragePercent(summary), summary.CoveredStatements, summary.TotalStatements)
}

func trimCommandOutput(out []byte) string {
	const max = 12000
	text := strings.TrimSpace(string(out))
	if len(text) <= max {
		return text
	}
	return text[len(text)-max:]
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
