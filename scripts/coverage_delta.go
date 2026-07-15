//go:build featuretools

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Blocks map[string]map[string]coverageBlock
}

type coverageBlock struct {
	File       string
	Range      string
	StartLine  int
	EndLine    int
	Statements int
	Covered    bool
}

type coveragePlan struct {
	CoverPackageDirs  []string
	TestPackageDirs   []string
	IntegrationSuites []string
	ImpactedFeature   map[string]bool
	ChangedLines      map[string]map[int]bool
	GlobalRelevant    bool
}

const featureCoverageRegressionToleranceStatements = 10

type listedPackage struct {
	ImportPath string
	Dir        string
	RepoDir    string
}

func main() {
	base := flag.String("base", defaultBaseRef(), "base git ref to compare")
	head := flag.String("head", "HEAD", "head git ref to compare")
	tags := flag.String("tags", "goolm,stdjson", "Go build tags for coverage runs")
	packages := flag.String("packages", "", "optional space-separated Go package patterns to force as test packages")
	integration := flag.Bool("integration", true, "include Docker-backed integration coverage when impacted features own integration suites")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail("coverage delta: %v", err)
	}
	if err := runCoverageDelta(root, *base, *head, *tags, strings.Fields(*packages), *integration); err != nil {
		fail("coverage delta: %v", err)
	}
}

func runCoverageDelta(root, base, head, tags string, forcedPackages []string, includeIntegration bool) error {
	specs, err := loadFeatureSpecs(root)
	if err != nil {
		return err
	}
	plan, err := buildCoveragePlan(root, base, head, specs, forcedPackages)
	if err != nil {
		return err
	}
	if !plan.GlobalRelevant {
		fmt.Println("coverage delta: skipped; no Go coverage-relevant changes")
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "picoclaw-coverage-delta-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	baseProfile, err := coverageForRef(root, tmpDir, "base", base, tags, plan, includeIntegration)
	if err != nil {
		return err
	}
	headProfile, err := coverageForRef(root, tmpDir, "head", head, tags, plan, includeIntegration)
	if err != nil {
		return err
	}

	failures := compareCoverage(specs, plan, baseProfile, headProfile)
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("%d failure(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}

	fmt.Printf("coverage delta: scoped global %s -> %s; %s; feature coverage ok\n",
		formatCoverage(baseProfile.Global),
		formatCoverage(headProfile.Global),
		changedLineStatus(plan.ChangedLines),
	)
	return nil
}

func buildCoveragePlan(root, base, head string, specs []featureSpecMetadata, forcedPackages []string) (coveragePlan, error) {
	changed, err := changedFiles(root, base, head)
	if err != nil {
		return coveragePlan{}, err
	}
	changedLines, err := changedGoLines(root, base, head)
	if err != nil {
		return coveragePlan{}, err
	}

	plan := coveragePlan{
		ImpactedFeature: make(map[string]bool),
		ChangedLines:    changedLines,
	}
	coverDirs := make(map[string]bool)
	testDirs := make(map[string]bool)
	suites := make(map[string]bool)

	if len(forcedPackages) > 0 {
		plan.GlobalRelevant = true
		for _, pkg := range forcedPackages {
			if strings.HasPrefix(pkg, "./") {
				dir := normalizeRepoPath(strings.TrimPrefix(pkg, "./"))
				if dir == "." {
					dir = ""
				}
				coverDirs[dir] = true
				testDirs[dir] = true
			}
		}
	}

	for _, path := range changed {
		path = normalizeRepoPath(path)
		if isCoverageRelevantChange(path) {
			plan.GlobalRelevant = true
		}
		if isCoverageRelevantGoFile(path) {
			testDirs[normalizeRepoPath(filepath.Dir(path))] = true
			if !strings.HasSuffix(path, "_test.go") {
				coverDirs[normalizeRepoPath(filepath.Dir(path))] = true
			}
		}
		if !isGoProductionCoverageFile(path) || !isProductionCodePath(path) {
			continue
		}
		for _, owner := range codeOwnersForPath(specs, path) {
			plan.ImpactedFeature[owner.SpecRelPath] = true
		}
	}

	for _, spec := range specs {
		if !plan.ImpactedFeature[spec.RelPath] {
			continue
		}
		for _, dir := range featureOwnedGoPackageDirs(root, spec) {
			coverDirs[dir] = true
			testDirs[dir] = true
		}
		for _, dir := range evidenceTestPackageDirs(root, spec) {
			testDirs[dir] = true
		}
		for _, suite := range featureIntegrationSuites(root, spec) {
			suites[suite] = true
		}
	}

	if touchesGoModule(changed) {
		for _, dir := range allGoPackageDirs(root) {
			coverDirs[dir] = true
			testDirs[dir] = true
		}
	}

	if len(testDirs) == 0 {
		for dir := range coverDirs {
			testDirs[dir] = true
		}
	}

	plan.CoverPackageDirs = sortedKeys(coverDirs)
	plan.TestPackageDirs = sortedKeys(testDirs)
	plan.IntegrationSuites = sortedKeys(suites)
	return plan, nil
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
	return isCoverageRelevantGoFile(path)
}

func isCoverageRelevantGoFile(path string) bool {
	path = normalizeRepoPath(path)
	if !strings.HasSuffix(path, ".go") || isIgnoredProductionPath(path) {
		return false
	}
	if strings.HasPrefix(path, "cmd/") ||
		strings.HasPrefix(path, "pkg/") ||
		strings.HasPrefix(path, "web/backend/") ||
		strings.HasPrefix(path, "integration/") {
		return true
	}
	return false
}

func touchesGoModule(changed []string) bool {
	for _, path := range changed {
		if path == "go.mod" || path == "go.sum" {
			return true
		}
	}
	return false
}

func featureOwnedGoPackageDirs(root string, spec featureSpecMetadata) []string {
	dirs := make(map[string]bool)
	for _, file := range allGoFiles(root) {
		if !isGoProductionCoverageFile(file) {
			continue
		}
		for _, owner := range spec.Ownerships {
			if owner.Kind == "CODE" && codePatternMatches(owner.Pattern, file) {
				dirs[normalizeRepoPath(filepath.Dir(file))] = true
				break
			}
		}
	}
	return sortedKeys(dirs)
}

func evidenceTestPackageDirs(root string, spec featureSpecMetadata) []string {
	re := regexpMarkdownLink()
	dirs := make(map[string]bool)
	evidence := markdownSection(spec.Text, "## Acceptance Evidence")
	for _, match := range re.FindAllStringSubmatch(evidence, -1) {
		target := strings.TrimSpace(match[1])
		if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "#") {
			continue
		}
		if hash := strings.IndexByte(target, '#'); hash >= 0 {
			target = target[:hash]
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(spec.Path), filepath.FromSlash(target)))
		relPath, err := filepath.Rel(root, resolved)
		if err != nil {
			continue
		}
		relPath = normalizeRepoPath(relPath)
		if strings.HasSuffix(relPath, "_test.go") {
			dirs[normalizeRepoPath(filepath.Dir(relPath))] = true
		}
	}
	return sortedKeys(dirs)
}

func featureIntegrationSuites(root string, spec featureSpecMetadata) []string {
	allSuites := integrationSuiteNames(root)
	suites := make(map[string]bool)
	for _, owner := range spec.Ownerships {
		if owner.Kind != "INTEGRATION" {
			continue
		}
		pattern := normalizeRepoPathPattern(owner.Pattern)
		if pattern == "*" || pattern == "integration/**" || pattern == "integration/*" {
			for _, suite := range allSuites {
				suites[suite] = true
			}
			continue
		}
		for _, suite := range allSuites {
			if globMatch(pattern, suite) || globMatch(pattern, "INTEGRATION "+suite) {
				suites[suite] = true
			}
		}
	}

	re := regexpMarkdownLink()
	for _, match := range re.FindAllStringSubmatch(markdownSection(spec.Text, "## Acceptance Evidence"), -1) {
		target := normalizeRepoPath(match[1])
		parts := strings.Split(target, "/")
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] == "integration" && parts[i+1] == "suites" {
				suites[parts[i+2]] = true
			}
		}
	}
	return sortedKeys(suites)
}

func integrationSuiteNames(root string) []string {
	base := filepath.Join(root, "integration", "suites")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var suites []string
	for _, entry := range entries {
		if entry.IsDir() {
			suites = append(suites, entry.Name())
		}
	}
	sort.Strings(suites)
	return suites
}

func allGoPackageDirs(root string) []string {
	dirs := make(map[string]bool)
	for _, file := range allGoFiles(root) {
		dirs[normalizeRepoPath(filepath.Dir(file))] = true
	}
	return sortedKeys(dirs)
}

func allGoFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "build", "dist", "node_modules", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		files = append(files, rel(root, path))
		return nil
	})
	sort.Strings(files)
	return files
}

func coverageForRef(root, tmpDir, label, ref, tags string, plan coveragePlan, includeIntegration bool) (coverageProfile, error) {
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

	packages, err := listGoPackages(worktree, tags)
	if err != nil {
		return coverageProfile{}, err
	}
	coverImports := importPathsForDirs(packages, plan.CoverPackageDirs)
	testImports := importPathsForDirs(packages, plan.TestPackageDirs)
	if len(testImports) == 0 {
		testImports = coverImports
	}
	if len(testImports) == 0 {
		return emptyCoverageProfile(), nil
	}

	profilePath := filepath.Join(tmpDir, label+".cover.out")
	profile, err := runGoCoverage(worktree, label, ref, tags, profilePath, coverImports, testImports)
	if err != nil {
		return coverageProfile{}, err
	}

	if includeIntegration && len(plan.IntegrationSuites) > 0 && len(coverImports) > 0 {
		integrationProfile, err := runIntegrationCoverage(worktree, label, ref, tags, coverImports, plan.IntegrationSuites)
		if err != nil {
			return coverageProfile{}, err
		}
		profile = mergeCoverageProfiles(profile, integrationProfile)
	}

	return profile, nil
}

func runGoCoverage(worktree, label, ref, tags, profilePath string, coverImports, testImports []string) (coverageProfile, error) {
	args := []string{"test", "-buildvcs=false"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-covermode=atomic", "-coverprofile", profilePath)
	if len(coverImports) > 0 {
		args = append(args, "-coverpkg", strings.Join(coverImports, ","))
	}
	args = append(args, testImports...)
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

func runIntegrationCoverage(worktree, label, ref, tags string, coverImports, suites []string) (coverageProfile, error) {
	coverDir := filepath.Join(worktree, ".coverage", "integration-"+label)
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return coverageProfile{}, fmt.Errorf("create integration coverage dir: %w", err)
	}

	args := append([]string{filepath.Join(worktree, "scripts", "run-integration-tests.sh")}, suites...)
	cmd := exec.Command("bash", args...)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(),
		"GOTOOLCHAIN=auto",
		"INTEGRATION_COVERPKG="+strings.Join(coverImports, ","),
		"INTEGRATION_COVERPROFILE_DIR=/workspace/.coverage/integration-"+label,
	)
	if tags != "" {
		cmd.Env = append(cmd.Env, "GOFLAGS=-tags="+tags+",integration")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return coverageProfile{}, fmt.Errorf("integration coverage for %s (%s): %w\n%s", label, ref, err, trimCommandOutput(out))
	}

	modulePath, err := modulePath(worktree)
	if err != nil {
		return coverageProfile{}, err
	}
	profile := emptyCoverageProfile()
	err = filepath.WalkDir(coverDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cover.out") {
			return nil
		}
		next, err := parseCoverageProfile(worktree, modulePath, path)
		if err != nil {
			return err
		}
		profile = mergeCoverageProfiles(profile, next)
		return nil
	})
	if err != nil {
		return coverageProfile{}, fmt.Errorf("parse integration coverage: %w", err)
	}
	return profile, nil
}

func listGoPackages(root, tags string) (map[string]listedPackage, error) {
	args := []string{"list", "-json", "-buildvcs=false"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	decoder := json.NewDecoder(bytes.NewReader(out))
	packages := make(map[string]listedPackage)
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		relDir, err := filepath.Rel(root, pkg.Dir)
		if err != nil {
			continue
		}
		pkg.RepoDir = normalizeRepoPath(relDir)
		if pkg.RepoDir == "." {
			pkg.RepoDir = ""
		}
		packages[pkg.RepoDir] = pkg
	}
	return packages, nil
}

func importPathsForDirs(packages map[string]listedPackage, dirs []string) []string {
	seen := make(map[string]bool)
	var imports []string
	for _, dir := range dirs {
		dir = normalizeRepoPath(dir)
		if dir == "." {
			dir = ""
		}
		pkg, ok := packages[dir]
		if !ok || pkg.ImportPath == "" || seen[pkg.ImportPath] {
			continue
		}
		seen[pkg.ImportPath] = true
		imports = append(imports, pkg.ImportPath)
	}
	sort.Strings(imports)
	return imports
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

	profile := emptyCoverageProfile()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		block, err := parseCoverageBlock(root, modulePath, line)
		if err != nil {
			return coverageProfile{}, err
		}
		addCoverageBlock(profile, block)
	}
	if err := scanner.Err(); err != nil {
		return coverageProfile{}, err
	}
	return summarizeCoverageBlocks(profile), nil
}

func parseCoverageBlock(root, modulePath, line string) (coverageBlock, error) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return coverageBlock{}, fmt.Errorf("invalid coverage line %q", line)
	}
	filePath := coverageFileToRepoPath(root, modulePath, line[:colon])
	fields := strings.Fields(line[colon+1:])
	if len(fields) != 3 {
		return coverageBlock{}, fmt.Errorf("invalid coverage fields %q", line)
	}
	startLine, endLine, err := coverageRangeLines(fields[0])
	if err != nil {
		return coverageBlock{}, fmt.Errorf("invalid coverage range in %q: %w", line, err)
	}
	statements, err := strconv.Atoi(fields[1])
	if err != nil {
		return coverageBlock{}, fmt.Errorf("invalid statement count in %q: %w", line, err)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return coverageBlock{}, fmt.Errorf("invalid coverage count in %q: %w", line, err)
	}
	return coverageBlock{
		File:       filePath,
		Range:      fields[0],
		StartLine:  startLine,
		EndLine:    endLine,
		Statements: statements,
		Covered:    count > 0,
	}, nil
}

func coverageRangeLines(value string) (int, int, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected start,end")
	}
	start, err := coveragePointLine(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := coveragePointLine(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func coveragePointLine(value string) (int, error) {
	line, _, ok := strings.Cut(value, ".")
	if !ok {
		return 0, fmt.Errorf("expected line.column")
	}
	return strconv.Atoi(line)
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

func emptyCoverageProfile() coverageProfile {
	return coverageProfile{
		Files:  make(map[string]coverageSummary),
		Blocks: make(map[string]map[string]coverageBlock),
	}
}

func addCoverageBlock(profile coverageProfile, block coverageBlock) {
	if profile.Blocks[block.File] == nil {
		profile.Blocks[block.File] = make(map[string]coverageBlock)
	}
	key := block.Range
	existing, ok := profile.Blocks[block.File][key]
	if ok {
		existing.Covered = existing.Covered || block.Covered
		profile.Blocks[block.File][key] = existing
		return
	}
	profile.Blocks[block.File][key] = block
}

func mergeCoverageProfiles(a, b coverageProfile) coverageProfile {
	merged := emptyCoverageProfile()
	for _, profile := range []coverageProfile{a, b} {
		for _, blocks := range profile.Blocks {
			for _, block := range blocks {
				addCoverageBlock(merged, block)
			}
		}
	}
	return summarizeCoverageBlocks(merged)
}

func summarizeCoverageBlocks(profile coverageProfile) coverageProfile {
	profile.Global = coverageSummary{}
	profile.Files = make(map[string]coverageSummary)
	for file, blocks := range profile.Blocks {
		var fileSummary coverageSummary
		for _, block := range blocks {
			fileSummary.TotalStatements += block.Statements
			profile.Global.TotalStatements += block.Statements
			if block.Covered {
				fileSummary.CoveredStatements += block.Statements
				profile.Global.CoveredStatements += block.Statements
			}
		}
		profile.Files[file] = fileSummary
	}
	return profile
}

func compareCoverage(specs []featureSpecMetadata, plan coveragePlan, baseProfile, headProfile coverageProfile) []string {
	var failures []string
	if summaryRegressed(baseProfile.Global, headProfile.Global) {
		failures = append(failures, fmt.Sprintf(
			"scoped Go statement coverage decreased: %s -> %s",
			formatCoverage(baseProfile.Global),
			formatCoverage(headProfile.Global),
		))
	}

	baseFeature := featureCoverage(specs, baseProfile)
	headFeature := featureCoverage(specs, headProfile)
	for _, spec := range specs {
		if !plan.ImpactedFeature[spec.RelPath] {
			continue
		}
		baseSummary := baseFeature[spec.RelPath]
		headSummary := headFeature[spec.RelPath]
		if headSummary.TotalStatements == 0 {
			continue
		}
		if featureSummaryRegressed(baseSummary, headSummary) {
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

	return failures
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

func changedLineCoverageFailures(changedLines map[string]map[int]bool, profile coverageProfile) []string {
	var failures []string
	for file, lines := range changedLines {
		if !isGoProductionCoverageFile(file) || !isProductionCodePath(file) {
			continue
		}
		blocks := profile.Blocks[file]
		if len(blocks) == 0 {
			continue
		}
		for _, line := range sortedLineNumbers(lines) {
			matching := blocksForLine(blocks, line)
			if len(matching) == 0 {
				continue
			}
			covered := false
			for _, block := range matching {
				if block.Covered {
					covered = true
					break
				}
			}
			if !covered {
				failures = append(failures, fmt.Sprintf("%s:%d changed executable line is not covered", file, line))
			}
		}
	}
	return failures
}

func changedLineStatus(changedLines map[string]map[int]bool) string {
	total := 0
	for file, lines := range changedLines {
		if !isGoProductionCoverageFile(file) || !isProductionCodePath(file) {
			continue
		}
		total += len(lines)
	}
	if total == 0 {
		return "no changed production Go lines"
	}
	return fmt.Sprintf("%d changed production Go line(s) covered", total)
}

func blocksForLine(blocks map[string]coverageBlock, line int) []coverageBlock {
	var matching []coverageBlock
	for _, block := range blocks {
		if line >= block.StartLine && line <= block.EndLine {
			matching = append(matching, block)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].StartLine != matching[j].StartLine {
			return matching[i].StartLine < matching[j].StartLine
		}
		return matching[i].EndLine < matching[j].EndLine
	})
	return matching
}

func changedGoLines(root, base, head string) (map[string]map[int]bool, error) {
	out, err := gitOutput(root, "diff", "--unified=0", "--no-ext-diff", base+"..."+head, "--", "*.go")
	if err != nil {
		return nil, fmt.Errorf("git diff changed lines %s...%s: %w", base, head, err)
	}
	result := make(map[string]map[int]bool)
	var currentFile string
	newLine := 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = normalizeRepoPath(strings.TrimPrefix(line, "+++ b/"))
			continue
		}
		if strings.HasPrefix(line, "+++ /dev/null") {
			currentFile = ""
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			start, err := parseDiffNewStart(line)
			if err != nil {
				return nil, err
			}
			newLine = start
			continue
		}
		if currentFile == "" || strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- ") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			if result[currentFile] == nil {
				result[currentFile] = make(map[int]bool)
			}
			result[currentFile][newLine] = true
			newLine++
		case strings.HasPrefix(line, "-"):
		default:
			newLine++
		}
	}
	return result, nil
}

func parseDiffNewStart(hunk string) (int, error) {
	parts := strings.Split(hunk, " ")
	for _, part := range parts {
		if !strings.HasPrefix(part, "+") {
			continue
		}
		part = strings.TrimPrefix(part, "+")
		if comma := strings.IndexByte(part, ','); comma >= 0 {
			part = part[:comma]
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return 0, fmt.Errorf("parse hunk %q: %w", hunk, err)
		}
		return value, nil
	}
	return 0, fmt.Errorf("parse hunk %q: missing new range", hunk)
}

func isGoProductionCoverageFile(path string) bool {
	path = normalizeRepoPath(path)
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !isIgnoredProductionPath(path)
}

func summaryRegressed(base, head coverageSummary) bool {
	return head.CoveredStatements < base.CoveredStatements
}

func featureSummaryRegressed(base, head coverageSummary) bool {
	return head.CoveredStatements+featureCoverageRegressionToleranceStatements < base.CoveredStatements
}

func coveragePercent(summary coverageSummary) float64 {
	if summary.TotalStatements == 0 {
		return 100
	}
	return float64(summary.CoveredStatements) * 100 / float64(summary.TotalStatements)
}

func formatCoverage(summary coverageSummary) string {
	return fmt.Sprintf("%.2f%% (%d/%d)", coveragePercent(summary), summary.CoveredStatements, summary.TotalStatements)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "" && key != "." {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedLineNumbers(values map[int]bool) []int {
	lines := make([]int, 0, len(values))
	for line := range values {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	return lines
}

func regexpMarkdownLink() *regexp.Regexp {
	return regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
}

func markdownSection(text, heading string) string {
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
