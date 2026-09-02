//go:build featuretools

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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

type goCachePaths struct {
	Build   string `json:"GOCACHE"`
	Modules string `json:"GOMODCACHE"`
}

type coveragePlan struct {
	CoverPackageDirs  []string
	TestPackageDirs   []string
	IntegrationSuites []string
	ImpactedFeature   map[string]bool
	ChangedLines      map[string]map[int]bool
	GlobalRelevant    bool
}

const (
	featureCoverageRegressionToleranceStatements = 10
	coverageNestedBenchmarkSkipPattern           = `^Test(GraderAcceptsReferenceAndReportsMutationEvidence|CodingAgentBenchmarkScriptedGatewayPath|WorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig)$`
	coverageGoTestParallelism                    = 1
)

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
	integration := flag.Bool(
		"integration",
		true,
		"include Docker-backed integration coverage when impacted features own integration suites",
	)
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

	fmt.Printf("coverage delta: scoped global %s -> %s (uncovered statement debt %d -> %d); %s; feature coverage ok\n",
		formatCoverage(baseProfile.Global),
		formatCoverage(headProfile.Global),
		uncoveredStatements(baseProfile.Global),
		uncoveredStatements(headProfile.Global),
		changedLineStatus(plan.ChangedLines),
	)
	return nil
}

func buildCoveragePlan(
	root, base, head string,
	specs []featureSpecMetadata,
	forcedPackages []string,
) (coveragePlan, error) {
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
		if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
			strings.HasPrefix(target, "#") {
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

func coverageForRef(
	root, tmpDir, label, ref, tags string,
	plan coveragePlan,
	includeIntegration bool,
) (coverageProfile, error) {
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

	coverageHome := filepath.Join(tmpDir, label+"-picoclaw-home")
	goCaches, err := resolveGoCachePaths(root, os.Environ())
	if err != nil {
		return coverageProfile{}, err
	}
	environment := coverageEnvironment(os.Environ(), coverageHome, goCaches)
	if err = prepareCoverageStorage(coverageHome); err != nil {
		return coverageProfile{}, fmt.Errorf("create %s coverage home: %w", label, err)
	}

	if err := runGoGenerate(worktree, label, ref, environment); err != nil {
		return coverageProfile{}, err
	}
	if err = buildCoverageTestBinary(worktree, label, ref, tags, environment); err != nil {
		return coverageProfile{}, err
	}

	packages, err := listGoPackages(worktree, tags, environment)
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
	profile, err := runGoCoverage(
		worktree,
		label,
		ref,
		tags,
		profilePath,
		coverImports,
		testImports,
		coverageFallbackHomeEnvironment(environment),
	)
	if err != nil {
		return coverageProfile{}, err
	}
	if err = writeCoverageConfig(coverageHome); err != nil {
		return coverageProfile{}, fmt.Errorf("write %s coverage config: %w", label, err)
	}

	if includeIntegration && len(plan.IntegrationSuites) > 0 && len(coverImports) > 0 {
		integrationSuites, err := coverageIntegrationSuitesForRef(
			worktree,
			label,
			ref,
			plan.IntegrationSuites,
		)
		if err != nil {
			return coverageProfile{}, err
		}
		if len(integrationSuites) == 0 {
			return profile, nil
		}
		integrationProfile, err := runIntegrationCoverage(
			worktree,
			label,
			ref,
			tags,
			coverImports,
			integrationSuites,
			environment,
		)
		if err != nil {
			return coverageProfile{}, err
		}
		profile = mergeCoverageProfiles(profile, integrationProfile)
	}

	return profile, nil
}

// coverageIntegrationSuitesForRef resolves a head-derived integration plan
// against one checked-out ref. A suite added by the head cannot exist in the
// immutable base, so base coverage omits only that absent directory. Head must
// contain every planned suite, and either ref still fails closed for an unsafe
// or non-directory path instead of silently bypassing a broken suite.
func coverageIntegrationSuitesForRef(
	worktree,
	label,
	ref string,
	planned []string,
) ([]string, error) {
	available := make([]string, 0, len(planned))
	for _, suite := range planned {
		if suite == "" || suite != strings.TrimSpace(suite) || strings.ContainsRune(suite, '\x00') ||
			suite == "." || suite == ".." || filepath.Base(suite) != suite ||
			strings.ContainsAny(suite, `/\`) {
			return nil, fmt.Errorf(
				"integration coverage for %s (%s): planned suite identity is invalid",
				label,
				ref,
			)
		}
		path := filepath.Join(worktree, "integration", "suites", suite)
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf(
					"integration coverage for %s (%s): suite %s is not a real directory",
					label,
					ref,
					suite,
				)
			}
			available = append(available, suite)
		case errors.Is(err, os.ErrNotExist) && label == "base":
			fmt.Printf(
				"coverage delta: base %s omits head-only integration suite %s\n",
				ref,
				suite,
			)
		case errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf(
				"integration coverage for %s (%s): planned suite %s is missing",
				label,
				ref,
				suite,
			)
		default:
			return nil, fmt.Errorf(
				"integration coverage for %s (%s): inspect suite %s: %w",
				label,
				ref,
				suite,
				err,
			)
		}
	}
	return available, nil
}

func runGoCoverage(
	worktree, label, ref, tags, profilePath string,
	coverImports, testImports []string,
	environment []string,
) (coverageProfile, error) {
	// A repository-wide -coverpkg build produces large instrumented binaries
	// and counter mappings. Bound concurrent package processes so small CI
	// disks cannot truncate a live mapping and surface a spurious SIGBUS.
	args := []string{
		"test",
		"-buildvcs=false",
		"-p",
		strconv.Itoa(coverageGoTestParallelism),
	}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-covermode=atomic", "-coverprofile", profilePath)
	// These tests spawn full external graders with nested normal and race test
	// processes. Running both inside the repository-wide atomic coverage command
	// can exhaust a shared runner and produce incomplete grader evidence.
	// Ordinary and race CI execute both tests directly; coverage retains every
	// other test in their packages.
	args = append(args, "-skip", coverageNestedBenchmarkSkipPattern)
	if len(coverImports) > 0 {
		args = append(args, "-coverpkg", strings.Join(coverImports, ","))
	}
	args = append(args, testImports...)
	run := func() ([]byte, error) {
		cmd := exec.Command("go", args...)
		cmd.Dir = worktree
		cmd.Env = append([]string(nil), environment...)
		return cmd.CombinedOutput()
	}
	// The guard tests detached base and head worktrees. A synchronization fix
	// in head cannot change a known historical base test flake, so retry only an
	// exact recognized failure from base once. Head failures are always final.
	out, err, retried := runCoverageCommandWithBaselineRetry(label, run)
	if retried {
		fmt.Fprintf(
			os.Stderr,
			"coverage delta: retried %s (%s) after known baseline test flake\n",
			label,
			ref,
		)
	}
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

func runCoverageCommandWithBaselineRetry(
	label string,
	run func() ([]byte, error),
) ([]byte, error, bool) {
	out, err := run()
	if err == nil || label != "base" || !isKnownCoverageBaselineFlake(out) {
		return out, err, false
	}
	out, err = run()
	return out, err, true
}

func isKnownCoverageBaselineFlake(out []byte) bool {
	return isKnownCoverageTempDirCleanupRace(out) ||
		isKnownRepositoryModelEvaluationCancellationRace(out) ||
		isKnownEvolutionDraftPersistenceTimeout(out) ||
		isKnownRepositoryReviewAutoContinueCompletionTimeout(out) ||
		isKnownRepositoryReviewSQLiteCompanionDisappearance(out)
}

// isKnownRepositoryReviewSQLiteCompanionDisappearance recognizes the one
// shared immutable-base sqlitestore race rather than any assertion callsite.
// Binding the sole diagnostic to its failed test's Go TempDir prevents the
// same low-level text in unrelated output from authorizing a retry.
func isKnownRepositoryReviewSQLiteCompanionDisappearance(out []byte) bool {
	const (
		failedPackage = "github.com/sipeed/picoclaw/web/backend/api"
	)
	var (
		failureMarkers   []string
		diagnostics      []string
		diagnosticLines  []int
		lstatLines       []string
		lstatLineIndexes []int
		packageFailures  int
		failurePackage   string
		lineIndex        int
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "_test.go:") && !coverageGoTestDiagnostic(line) {
			return false
		}
		if strings.HasPrefix(line, "lstat ") {
			lstatLines = append(lstatLines, line)
			lstatLineIndexes = append(lstatLineIndexes, lineIndex)
		}
		if strings.HasPrefix(line, "--- FAIL:") {
			name, ok := coverageFailedTestName(line)
			if !ok {
				return false
			}
			failureMarkers = append(failureMarkers, name)
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "FAIL" {
			packageFailures++
			failurePackage = fields[1]
		}
		if coverageGoTestDiagnostic(line) {
			diagnostics = append(diagnostics, line)
			diagnosticLines = append(diagnosticLines, lineIndex)
		}
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") ||
			strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]") {
			return false
		}
		lineIndex++
	}
	if scanner.Err() != nil || packageFailures != 1 || failurePackage != failedPackage ||
		len(diagnostics) != 1 || len(diagnosticLines) != 1 {
		return false
	}
	failedTest, ok := repositoryReviewCoverageFailureLeaf(failureMarkers)
	if !ok {
		return false
	}
	diagnostic, ok := repositoryReviewSQLiteCompanionDiagnostic(diagnostics[0])
	if !ok {
		return false
	}
	path, diagnosticCompanion, needsContinuation, ok := repositoryReviewSQLiteCompanionErrorPath(diagnostic)
	if !ok {
		return false
	}
	if needsContinuation {
		if len(lstatLines) != 1 || len(lstatLineIndexes) != 1 ||
			lstatLineIndexes[0] != diagnosticLines[0]+1 {
			return false
		}
		const (
			continuationPrefix = "lstat "
			continuationSuffix = ": no such file or directory"
		)
		if !strings.HasPrefix(lstatLines[0], continuationPrefix) ||
			!strings.HasSuffix(lstatLines[0], continuationSuffix) {
			return false
		}
		path = strings.TrimSuffix(
			strings.TrimPrefix(lstatLines[0], continuationPrefix),
			continuationSuffix,
		)
	} else if len(lstatLines) != 0 || len(lstatLineIndexes) != 0 {
		return false
	}
	pathCompanion, ok := safeRepositoryReviewSQLiteCompanionPath(path, failedTest)
	return ok && (diagnosticCompanion == "" || diagnosticCompanion == pathCompanion)
}

func repositoryReviewCoverageFailureLeaf(failureMarkers []string) (string, bool) {
	if len(failureMarkers) == 0 || len(failureMarkers) > 4 {
		return "", false
	}
	for index, name := range failureMarkers {
		if !safeRepositoryReviewCoverageTestName(name) {
			return "", false
		}
		if index == 0 {
			if strings.Contains(name, "/") {
				return "", false
			}
			continue
		}
		prefix := failureMarkers[index-1] + "/"
		if !strings.HasPrefix(name, prefix) ||
			strings.Contains(strings.TrimPrefix(name, prefix), "/") {
			return "", false
		}
	}
	return failureMarkers[len(failureMarkers)-1], true
}

func safeRepositoryReviewCoverageTestName(name string) bool {
	if !strings.HasPrefix(name, "TestRepositoryReview") || len(name) > 256 {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '/' {
			continue
		}
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" {
			return false
		}
	}
	return true
}

func repositoryReviewSQLiteCompanionDiagnostic(line string) (string, bool) {
	const (
		filePrefix = "repository_review_"
		fileSuffix = "_test.go"
	)
	goSuffix := strings.Index(line, ".go:")
	if goSuffix < 0 {
		return "", false
	}
	file := line[:goSuffix+len(".go")]
	if !strings.HasPrefix(file, filePrefix) || !strings.HasSuffix(file, fileSuffix) ||
		strings.ContainsAny(file, `/\`) {
		return "", false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(file, filePrefix), fileSuffix)
	if stem == "" {
		return "", false
	}
	for _, character := range stem {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' {
			continue
		}
		return "", false
	}
	lineAndDiagnostic := line[goSuffix+len(".go:"):]
	separator := strings.Index(lineAndDiagnostic, ": ")
	if separator <= 0 {
		return "", false
	}
	lineNumber := lineAndDiagnostic[:separator]
	if lineNumber == "0" || !coverageCanonicalUint32Decimal(lineNumber) {
		return "", false
	}
	diagnostic := lineAndDiagnostic[separator+2:]
	const securePrefix = "secure repository-reviews database files: "
	if strings.HasPrefix(diagnostic, securePrefix) {
		return diagnostic, true
	}
	preamble, diagnostic, found := strings.Cut(diagnostic, " = ")
	if !found || !safeRepositoryReviewSQLiteAssertionPreamble(preamble) ||
		!strings.HasPrefix(diagnostic, securePrefix) {
		return "", false
	}
	return diagnostic, true
}

func safeRepositoryReviewSQLiteAssertionPreamble(preamble string) bool {
	if len(preamble) == 0 || len(preamble) > 96 || preamble != strings.TrimSpace(preamble) ||
		!strings.HasSuffix(preamble, " error") {
		return false
	}
	for _, character := range preamble {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == ' ' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func repositoryReviewSQLiteCompanionErrorPath(
	diagnostic string,
) (path, companion string, needsContinuation, ok bool) {
	const (
		securePrefix = "secure repository-reviews database files: "
		notExist     = ": no such file or directory"
	)
	errorText := strings.TrimPrefix(diagnostic, securePrefix)
	if errorText == diagnostic || errorText == "" {
		return "", "", false, false
	}
	if strings.HasPrefix(errorText, "open ") && strings.HasSuffix(errorText, notExist) {
		path = strings.TrimSuffix(strings.TrimPrefix(errorText, "open "), notExist)
		return path, "", false, path != ""
	}
	if errorText == "private file changed while securing" {
		return "", "", true, true
	}
	const changedWhileOpening = " changed while opening"
	if strings.HasSuffix(errorText, changedWhileOpening) {
		companion = strings.TrimSuffix(errorText, changedWhileOpening)
		if companion == "repository-reviews.db-wal" || companion == "repository-reviews.db-shm" {
			return "", companion, true, true
		}
	}
	return "", "", false, false
}

func safeRepositoryReviewSQLiteCompanionPath(path, failedTest string) (string, bool) {
	if path == "" || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path {
		return "", false
	}
	tempRoot := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(tempRoot, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 9 || parts[1] != "base-picoclaw-home" || parts[2] != ".tmp" ||
		parts[4] != "tmp" || parts[6] != "001" || parts[7] != "repository_reviews" ||
		(parts[8] != "repository-reviews.db-wal" && parts[8] != "repository-reviews.db-shm") {
		return "", false
	}
	const (
		coveragePrefix   = "picoclaw-coverage-delta-"
		apiRuntimePrefix = "picoclaw-api-test-runtime-"
	)
	testPrefix, ok := repositoryReviewCoverageTempDirPrefix(failedTest)
	if !ok {
		return "", false
	}
	valid := strings.HasPrefix(parts[0], coveragePrefix) &&
		coverageCanonicalUint32Decimal(strings.TrimPrefix(parts[0], coveragePrefix)) &&
		strings.HasPrefix(parts[3], apiRuntimePrefix) &&
		coverageCanonicalUint32Decimal(strings.TrimPrefix(parts[3], apiRuntimePrefix)) &&
		strings.HasPrefix(parts[5], testPrefix) &&
		coverageCanonicalUint32Decimal(strings.TrimPrefix(parts[5], testPrefix))
	return parts[8], valid
}

func repositoryReviewCoverageTempDirPrefix(failedTest string) (string, bool) {
	if !safeRepositoryReviewCoverageTestName(failedTest) {
		return "", false
	}
	// testing.T.TempDir truncates the test name to 64 bytes before dropping
	// path separators, then os.MkdirTemp appends one uint32 decimal suffix.
	// Test identities admitted above are ASCII, so byte truncation is exact.
	pattern := failedTest
	if len(pattern) > 64 {
		pattern = pattern[:64]
	}
	return strings.ReplaceAll(pattern, "/", ""), true
}

func isKnownRepositoryReviewAutoContinueCompletionTimeout(out []byte) bool {
	const (
		failedTest       = "TestRepositoryReviewAutomationAutoContinueReusesResolvedCommit"
		failedPackage    = "github.com/sipeed/picoclaw/web/backend/api"
		diagnosticPrefix = "repository_review_automations_test.go:1897: automation "
		diagnosticSuffix = " did not reach completed"
	)
	var (
		failureMarkers  []string
		diagnostics     []string
		packageFailures int
		failurePackage  string
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "--- FAIL:") {
			name, ok := coverageFailedTestName(line)
			if !ok {
				return false
			}
			failureMarkers = append(failureMarkers, name)
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "FAIL" {
			packageFailures++
			failurePackage = fields[1]
		}
		if coverageGoTestDiagnostic(line) {
			diagnostics = append(diagnostics, line)
		}
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") ||
			strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]") {
			return false
		}
	}
	if scanner.Err() != nil || len(failureMarkers) != 1 || failureMarkers[0] != failedTest ||
		packageFailures != 1 || failurePackage != failedPackage || len(diagnostics) != 1 ||
		!strings.HasPrefix(diagnostics[0], diagnosticPrefix) ||
		!strings.HasSuffix(diagnostics[0], diagnosticSuffix) {
		return false
	}
	automationID := strings.TrimSuffix(
		strings.TrimPrefix(diagnostics[0], diagnosticPrefix),
		diagnosticSuffix,
	)
	return isCoverageGeneratedRepositoryReviewAutomationID(automationID)
}

func isCoverageGeneratedRepositoryReviewAutomationID(id string) bool {
	const prefix = "rra_"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+26 {
		return false
	}
	for _, character := range strings.TrimPrefix(id, prefix) {
		if character < 'a' || character > 'z' {
			if character < '2' || character > '7' {
				return false
			}
		}
	}
	return true
}

func isKnownEvolutionDraftPersistenceTimeout(out []byte) bool {
	const (
		failedTest    = "TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator"
		failedPackage = "github.com/sipeed/picoclaw/pkg/agent"
		diagnostic    = "evolution_bridge_test.go:596: timed out waiting for 1 drafts at "
	)
	var (
		failureMarkers  []string
		diagnostics     []string
		packageFailures int
		failurePackage  string
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "--- FAIL:") {
			name, ok := coverageFailedTestName(line)
			if !ok {
				return false
			}
			failureMarkers = append(failureMarkers, name)
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "FAIL" {
			packageFailures++
			failurePackage = fields[1]
		}
		if coverageGoTestDiagnostic(line) {
			diagnostics = append(diagnostics, line)
		}
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") ||
			strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]") {
			return false
		}
	}
	if scanner.Err() != nil || len(failureMarkers) != 1 || failureMarkers[0] != failedTest ||
		packageFailures != 1 || failurePackage != failedPackage || len(diagnostics) != 1 ||
		!strings.HasPrefix(diagnostics[0], diagnostic) {
		return false
	}
	return isSafeEvolutionDraftTimeoutPath(strings.TrimPrefix(diagnostics[0], diagnostic))
}

func isSafeEvolutionDraftTimeoutPath(path string) bool {
	if path == "" || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path {
		return false
	}
	tempRoot := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(tempRoot, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 8 || parts[1] != "base-picoclaw-home" || parts[2] != ".tmp" ||
		parts[5] != "state" || parts[6] != "evolution" || parts[7] != "skill-drafts.json" {
		return false
	}
	const (
		coveragePrefix = "picoclaw-coverage-delta-"
		testPrefix     = "TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator"
	)
	return strings.HasPrefix(parts[0], coveragePrefix) &&
		coverageASCIIDigits(strings.TrimPrefix(parts[0], coveragePrefix)) &&
		strings.HasPrefix(parts[3], testPrefix) &&
		coverageASCIIDigits(strings.TrimPrefix(parts[3], testPrefix)) &&
		coverageASCIIDigits(parts[4])
}

func coverageASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func coverageCanonicalUint32Decimal(value string) bool {
	if !coverageASCIIDigits(value) || len(value) > 10 ||
		(len(value) > 1 && value[0] == '0') {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 32)
	return err == nil
}

func isKnownRepositoryModelEvaluationCancellationRace(out []byte) bool {
	const (
		batchCancellationTest     = "TestRepositoryModelEvaluationControllerBatchFailureAndRunningCancellation"
		batchCancellationSubtest  = batchCancellationTest + "/running_cancellation"
		cancellationRestartTest   = "TestRepositoryModelEvaluationControllerCancellationAndRestart"
		controllerTestFile        = "repository_model_evaluation_controller_test.go"
		evaluationsTestFile       = "repository_model_evaluations_test.go"
		failedPackage             = "github.com/sipeed/picoclaw/web/backend/api"
		staleTransitionDiagnostic = "cancel status=409 body={\"code\":\"stale_repository_model_evaluation\",\"message\":\"invalid repository evaluation status transition\"}"
		staleStateDiagnostic      = "cancel status=409 body={\"code\":\"stale_repository_model_evaluation\",\"message\":\"repository evaluation state changed\"}"
	)

	var (
		failureMarkers  []string
		diagnostics     []string
		packageFailures int
		failurePackage  string
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "--- FAIL:") {
			name, ok := coverageFailedTestName(line)
			if !ok {
				return false
			}
			failureMarkers = append(failureMarkers, name)
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "FAIL" {
			packageFailures++
			failurePackage = fields[1]
		}
		if coverageGoTestDiagnostic(line) {
			diagnostics = append(diagnostics, line)
		}
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") ||
			strings.Contains(line, "[build failed]") ||
			strings.Contains(line, "[setup failed]") {
			return false
		}
	}
	if scanner.Err() != nil || packageFailures != 1 || failurePackage != failedPackage ||
		len(diagnostics) != 1 {
		return false
	}

	switch {
	case len(failureMarkers) == 2 &&
		failureMarkers[0] == batchCancellationTest &&
		failureMarkers[1] == batchCancellationSubtest:
		return isExactCoverageTestDiagnostic(
			diagnostics[0],
			controllerTestFile,
			staleTransitionDiagnostic,
		)
	case len(failureMarkers) == 1 && failureMarkers[0] == cancellationRestartTest:
		return isExactCoverageTestDiagnostic(
			diagnostics[0],
			evaluationsTestFile,
			staleStateDiagnostic,
		)
	default:
		return false
	}
}

func coverageGoTestDiagnostic(line string) bool {
	goSuffix := strings.Index(line, ".go:")
	if goSuffix < 0 {
		return false
	}
	lineAndDiagnostic := line[goSuffix+len(".go:"):]
	separator := strings.Index(lineAndDiagnostic, ": ")
	if separator <= 0 {
		return false
	}
	for _, character := range lineAndDiagnostic[:separator] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func coverageFailedTestName(line string) (string, bool) {
	const prefix = "--- FAIL: "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	nameAndDuration := strings.TrimPrefix(line, prefix)
	durationIndex := strings.LastIndex(nameAndDuration, " (")
	if durationIndex <= 0 || !strings.HasSuffix(nameAndDuration, ")") {
		return "", false
	}
	return nameAndDuration[:durationIndex], true
}

func isExactCoverageTestDiagnostic(line, file, diagnostic string) bool {
	prefix := file + ":"
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	lineAndDiagnostic := strings.TrimPrefix(line, prefix)
	separator := strings.Index(lineAndDiagnostic, ": ")
	if separator <= 0 || lineAndDiagnostic[separator+2:] != diagnostic {
		return false
	}
	lineNumber := lineAndDiagnostic[:separator]
	for _, character := range lineNumber {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isKnownCoverageTempDirCleanupRace(out []byte) bool {
	const (
		agentFailurePrefix    = "--- FAIL: TestRunWorkerPanicReleasesSessionTurnState ("
		workflowFailurePrefix = "--- FAIL: TestHandleRunWorkflowStartsAsyncRun ("
		cleanupPrefix         = "TempDir RemoveAll cleanup:"
		agentCleanupSuffix    = "sessions: directory not empty"
		workflowCleanupPath   = "workflow_runs/wr_"
		cleanupSuffix         = ": directory not empty"
		agentPackage          = "github.com/sipeed/picoclaw/pkg/agent"
		workflowPackage       = "github.com/sipeed/picoclaw/web/backend/api"
	)

	var (
		agentFailures          int
		workflowFailures       int
		cleanupFailures        int
		agentCleanupFailures   int
		workflowCleanupFailure int
		packageFailures        int
		failedPackage          string
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "--- FAIL:") {
			switch {
			case strings.HasPrefix(line, agentFailurePrefix):
				agentFailures++
			case strings.HasPrefix(line, workflowFailurePrefix):
				workflowFailures++
			default:
				return false
			}
		}
		if _, diagnostic, found := strings.Cut(line, cleanupPrefix); found {
			cleanupFailures++
			diagnostic = strings.ReplaceAll(strings.TrimSpace(diagnostic), `\`, "/")
			switch {
			case strings.HasSuffix(diagnostic, "/"+agentCleanupSuffix):
				agentCleanupFailures++
			case isKnownWorkflowRunTempDirCleanupDiagnostic(
				diagnostic,
				workflowCleanupPath,
				cleanupSuffix,
			):
				workflowCleanupFailure++
			}
		}
		if coverageGoTestDiagnostic(line) && !strings.Contains(line, cleanupPrefix) {
			return false
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "FAIL" {
			packageFailures++
			failedPackage = fields[1]
		}
		// Any test-file diagnostic may be a functional assertion from the
		// target test or one of its helpers. Only testing.go may report the
		// recognized cleanup line.
		if strings.Contains(line, "_test.go:") {
			return false
		}
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") ||
			strings.Contains(line, "[build failed]") ||
			strings.Contains(line, "[setup failed]") {
			return false
		}
	}
	if scanner.Err() != nil {
		return false
	}
	return cleanupFailures == 1 && packageFailures == 1 &&
		((agentFailures == 1 && workflowFailures == 0 &&
			agentCleanupFailures == 1 && workflowCleanupFailure == 0 &&
			failedPackage == agentPackage) ||
			(workflowFailures == 1 && agentFailures == 0 &&
				workflowCleanupFailure == 1 && agentCleanupFailures == 0 &&
				failedPackage == workflowPackage))
}

func isKnownWorkflowRunTempDirCleanupDiagnostic(
	diagnostic string,
	pathMarker string,
	cleanupSuffix string,
) bool {
	if !strings.HasSuffix(diagnostic, cleanupSuffix) {
		return false
	}
	path := strings.TrimSuffix(diagnostic, cleanupSuffix)
	marker := "/" + pathMarker
	index := strings.LastIndex(path, marker)
	if index < 0 {
		return false
	}
	runID := path[index+len(marker):]
	if runID == "" {
		return false
	}
	for _, character := range runID {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func runIntegrationCoverage(
	worktree, label, ref, tags string,
	coverImports, suites, environment []string,
) (coverageProfile, error) {
	coverDir := filepath.Join(worktree, ".coverage", "integration-"+label)
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return coverageProfile{}, fmt.Errorf("create integration coverage dir: %w", err)
	}

	args := append([]string{filepath.Join(worktree, "scripts", "run-integration-tests.sh")}, suites...)
	cmd := exec.Command("bash", args...)
	cmd.Dir = worktree
	cmd.Env = append(append([]string(nil), environment...),
		"INTEGRATION_COVERPKG="+strings.Join(coverImports, ","),
		"INTEGRATION_COVERPROFILE_DIR=/workspace/.coverage/integration-"+label,
	)
	if tags != "" {
		cmd.Env = append(cmd.Env, "GOFLAGS=-tags="+tags+",integration")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return coverageProfile{}, fmt.Errorf(
			"integration coverage for %s (%s): %w\n%s",
			label,
			ref,
			err,
			trimCommandOutput(out),
		)
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

func listGoPackages(root, tags string, environment []string) (map[string]listedPackage, error) {
	args := []string{"list", "-json", "-buildvcs=false"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append([]string(nil), environment...)
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

func runGoGenerate(worktree, label, ref string, environment []string) error {
	cmd := exec.Command("go", "generate", "./...")
	cmd.Dir = worktree
	cmd.Env = append([]string(nil), environment...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go generate for %s (%s): %w\n%s", label, ref, err, trimCommandOutput(out))
	}
	return nil
}

func resolveGoCachePaths(root string, environment []string) (goCachePaths, error) {
	cmd := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE")
	cmd.Dir = root
	cmd.Env = make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, "GOTOOLCHAIN") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOTOOLCHAIN=auto")
	output, err := cmd.Output()
	if err != nil {
		return goCachePaths{}, fmt.Errorf("resolve Go cache paths: %w", err)
	}
	var paths goCachePaths
	if err := json.Unmarshal(output, &paths); err != nil {
		return goCachePaths{}, fmt.Errorf("decode Go cache paths: %w", err)
	}
	if strings.TrimSpace(paths.Build) == "" || strings.TrimSpace(paths.Modules) == "" {
		return goCachePaths{}, errors.New("go cache paths are incomplete")
	}
	return paths, nil
}

func coverageEnvironment(base []string, home string, caches goCachePaths) []string {
	environment := make([]string, 0, len(base)+16)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if ok && (upper == "HOME" || upper == "USERPROFILE" ||
			isAmbientTestCredentialOrAuthority(upper) ||
			strings.HasPrefix(upper, "PICOCLAW_") || strings.HasPrefix(upper, "XDG_") ||
			upper == "CODEX_HOME" || upper == "CLAUDE_CONFIG_DIR" || upper == "OPENCLAW_HOME" ||
			upper == "GNUPGHOME" || upper == "GIT_CONFIG_GLOBAL" ||
			upper == "GIT_CONFIG_NOSYSTEM" || upper == "TMPDIR" ||
			upper == "TEMP" || upper == "TMP" || upper == "DBUS_SESSION_BUS_ADDRESS" ||
			upper == "APPDATA" || upper == "LOCALAPPDATA" ||
			upper == "HOMEDRIVE" || upper == "HOMEPATH" ||
			strings.EqualFold(name, "GOCACHE") ||
			strings.EqualFold(name, "GOMODCACHE") ||
			strings.EqualFold(name, "GOTOOLCHAIN")) {
			continue
		}
		environment = append(environment, entry)
	}
	picoHome := filepath.Join(home, ".picoclaw")
	result := append(environment,
		"HOME="+home,
		"USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".xdg", "config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".xdg", "data"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".xdg", "cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".xdg", "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(home, ".xdg", "runtime"),
		"PICOCLAW_HOME="+picoHome,
		"PICOCLAW_CONFIG="+filepath.Join(picoHome, "config.json"),
		"PICOCLAW_BINARY="+filepath.Join(home, "bin", coverageExecutableName("picoclaw")),
		"CODEX_HOME="+filepath.Join(home, ".codex"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"),
		"OPENCLAW_HOME="+filepath.Join(home, ".openclaw"),
		"GNUPGHOME="+filepath.Join(home, ".gnupg"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"TMPDIR="+filepath.Join(home, ".tmp"),
		"TEMP="+filepath.Join(home, ".tmp"),
		"TMP="+filepath.Join(home, ".tmp"),
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(home, ".no-systemd-bus"),
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
		"AWS_EC2_METADATA_DISABLED=true",
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GOAUTH=off",
		"GOENV=off",
		"GOCACHE="+caches.Build,
		"GOMODCACHE="+caches.Modules,
		"GOTOOLCHAIN=auto",
	)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(home)
		result = append(
			result,
			"HOMEDRIVE="+volume,
			"HOMEPATH="+strings.TrimPrefix(home, volume),
		)
	}
	return result
}

// Historical base tests intentionally override HOME to exercise fallback
// discovery. Keep that semantic while running unit coverage: HOME is already
// disposable, and the explicit runtime config is published only after unit
// coverage, before any integration suite can launch the built product binary.
func coverageFallbackHomeEnvironment(environment []string) []string {
	result := make([]string, len(environment))
	for index, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (strings.EqualFold(name, "PICOCLAW_HOME") ||
			strings.EqualFold(name, "PICOCLAW_CONFIG")) {
			result[index] = name + "="
			continue
		}
		result[index] = entry
	}
	return result
}

func isAmbientTestCredentialOrAuthority(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, prefix := range []string{
		"AWS_",
		"AZURE_",
		"CLOUDSDK_",
		"COHERE_",
		"DEEPSEEK_",
		"GCLOUD_",
		"GEMINI_",
		"GOOGLE_",
		"GROQ_",
		"HF_",
		"HUGGINGFACE_",
		"HUGGING_FACE_",
		"MISTRAL_",
		"OCI_",
		"OPENAI_",
		"ANTHROPIC_",
		"LISTEN_",
		"SYSTEMD_",
		"VAULT_",
		"VERTEX_",
		"WATCHDOG_",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, suffix := range []string{
		"_ACCESS_KEY",
		"_API_KEY",
		"_API_KEYS",
		"_CREDENTIAL",
		"_CREDENTIALS",
		"_CREDENTIALS_FILE",
		"_JWT",
		"_JWT_V2",
		"_PAT",
		"_PASSWORD",
		"_PRIVATE_KEY",
		"_SECRET",
		"_SECRET_KEY",
		"_TOKEN",
		"_TOKEN_FILE",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	switch upper {
	case "API_KEY", "ACCESS_TOKEN", "AUTH_TOKEN", "CREDENTIALS", "GH_PAT", "GITHUB_PAT", "PASSWORD", "PAT",
		"PRIVATE_KEY", "REFRESH_TOKEN", "SECRET", "TOKEN",
		"BASH_ENV", "BOTO_CONFIG", "DOCKER_AUTH_CONFIG", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_HOST",
		"GCM_INTERACTIVE", "GIT_ASKPASS", "GIT_CEILING_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GIT_DIR",
		"GIT_EXEC_PATH", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_WORK_TREE",
		"GIT_TERMINAL_PROMPT", "GOAUTH", "GOENV",
		"GPG_AGENT_INFO", "KRB5CCNAME", "KRB5_CONFIG", "KUBECONFIG", "LD_LIBRARY_PATH", "LD_PRELOAD",
		"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "MYSQL_PWD", "NETRC", "NODE_AUTH_TOKEN", "NODE_OPTIONS",
		"NPM_CONFIG_USERCONFIG", "NPM_TOKEN", "PGPASSFILE", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
		"SSH_AUTH_SOCK", "SSLKEYLOGFILE":
		return true
	}
	return strings.HasPrefix(upper, "GIT_CONFIG_") || strings.HasPrefix(upper, "TF_TOKEN_")
}

func prepareCoverageHome(home string) error {
	if err := prepareCoverageStorage(home); err != nil {
		return err
	}
	return writeCoverageConfig(home)
}

func prepareCoverageStorage(home string) error {
	picoHome := filepath.Join(home, ".picoclaw")
	workspace := filepath.Join(picoHome, "workspace")
	eventDB := filepath.Join(workspace, "eventing", "events.db")
	for _, directory := range []string{
		home,
		filepath.Dir(eventDB),
		filepath.Join(home, ".xdg", "config"),
		filepath.Join(home, ".xdg", "data"),
		filepath.Join(home, ".xdg", "cache"),
		filepath.Join(home, ".xdg", "state"),
		filepath.Join(home, ".xdg", "runtime"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".openclaw"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".tmp"),
		filepath.Join(home, "bin"),
		filepath.Join(home, "AppData", "Roaming"),
		filepath.Join(home, "AppData", "Local"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	database, err := os.OpenFile(eventDB, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return database.Close()
}

func writeCoverageConfig(home string) error {
	picoHome := filepath.Join(home, ".picoclaw")
	workspace := filepath.Join(picoHome, "workspace")
	eventDB := filepath.Join(workspace, "eventing", "events.db")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		return err
	}
	configData, err := json.MarshalIndent(map[string]any{
		"agents":  map[string]any{"defaults": map[string]any{"workspace": workspace}},
		"gateway": map[string]any{"host": "127.0.0.1", "port": port},
		"events":  map[string]any{"ingress": map[string]any{"database_path": eventDB}},
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(picoHome, "config.json"), append(configData, '\n'), 0o600)
}

func buildCoverageTestBinary(
	worktree, label, ref, tags string,
	environment []string,
) error {
	binary := strings.TrimSpace(coverageEnvironmentValue(environment, "PICOCLAW_BINARY"))
	if binary == "" {
		return errors.New("coverage test binary path is unavailable")
	}
	arguments := []string{"build", "-buildvcs=false"}
	if strings.TrimSpace(tags) != "" {
		arguments = append(arguments, "-tags", tags)
	}
	arguments = append(arguments, "-o", binary, "./cmd/picoclaw")
	command := exec.Command("go", arguments...)
	command.Dir = worktree
	command.Env = append([]string(nil), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"build isolated test binary for %s (%s): %w\n%s",
			label,
			ref,
			err,
			trimCommandOutput(output),
		)
	}
	return nil
}

func coverageEnvironmentValue(environment []string, key string) string {
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

func coverageExecutableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
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

func compareCoverage(
	specs []featureSpecMetadata,
	plan coveragePlan,
	baseProfile, headProfile coverageProfile,
) []string {
	var failures []string
	if summaryRegressed(baseProfile.Global, headProfile.Global) {
		failures = append(failures, fmt.Sprintf(
			"scoped Go uncovered statement debt increased: %d -> %d (coverage %s -> %s)",
			uncoveredStatements(baseProfile.Global),
			uncoveredStatements(headProfile.Global),
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
				"%s Go uncovered statement debt increased: %d -> %d (coverage %s -> %s)",
				spec.RelPath,
				uncoveredStatements(baseSummary),
				uncoveredStatements(headSummary),
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
	return uncoveredStatements(head) > uncoveredStatements(base)
}

func featureSummaryRegressed(base, head coverageSummary) bool {
	return uncoveredStatements(head) >
		uncoveredStatements(base)+featureCoverageRegressionToleranceStatements
}

func uncoveredStatements(summary coverageSummary) int {
	return summary.TotalStatements - summary.CoveredStatements
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

	const tailBytes = 4000
	const separator = "\n... command output truncated; failure context preserved above ...\n"
	failureBytes := max - tailBytes - len(separator)
	failures := commandFailureExcerpt(text, failureBytes)
	if failures == "" {
		return commandOutputTail(text, max)
	}
	return failures + separator + commandOutputTail(text, tailBytes)
}

func commandFailureExcerpt(text string, maxBytes int) string {
	lines := strings.Split(text, "\n")
	markers := make([]int, 0)
	keep := make([]bool, len(lines))
	for index, line := range lines {
		if !isCommandFailureLine(line) {
			continue
		}
		markers = append(markers, index)
		start := index - 6
		if start < 0 {
			start = 0
		}
		end := index + 4
		if end > len(lines) {
			end = len(lines)
		}
		for contextIndex := start; contextIndex < end; contextIndex++ {
			keep[contextIndex] = true
		}
	}
	if len(markers) == 0 || maxBytes <= 0 {
		return ""
	}

	var excerpt strings.Builder
	excerpt.WriteString("failure markers:")
	for _, index := range markers {
		line := clipCommandFailureLine(lines[index])
		if excerpt.Len()+1+len(line) > maxBytes {
			break
		}
		excerpt.WriteByte('\n')
		excerpt.WriteString(line)
	}
	if excerpt.Len() == len("failure markers:") {
		return ""
	}

	contextHeader := "\nfailure context:"
	if excerpt.Len()+len(contextHeader) > maxBytes {
		return excerpt.String()
	}
	excerpt.WriteString(contextHeader)
	previous := -2
	for index, line := range lines {
		if !keep[index] || isCommandFailureLine(line) || isCommandCoverageNoise(line) {
			continue
		}
		line = clipCommandFailureLine(line)
		separator := "\n"
		if previous >= 0 && index != previous+1 {
			separator = "\n...\n"
		}
		if excerpt.Len()+len(separator)+len(line) > maxBytes {
			continue
		}
		excerpt.WriteString(separator)
		excerpt.WriteString(line)
		previous = index
	}
	return strings.TrimSpace(excerpt.String())
}

func clipCommandFailureLine(line string) string {
	const max = 512
	if len(line) <= max {
		return line
	}
	const separator = " ... "
	head := (max - len(separator)) / 2
	tail := max - len(separator) - head
	for head > 0 && !utf8.RuneStart(line[head]) {
		head--
	}
	tailStart := len(line) - tail
	for tailStart < len(line) && !utf8.RuneStart(line[tailStart]) {
		tailStart++
	}
	return line[:head] + separator + line[tailStart:]
}

func commandOutputTail(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func isCommandCoverageNoise(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "coverage:") ||
		((strings.HasPrefix(line, "ok\t") || strings.HasPrefix(line, "ok  \t")) &&
			strings.Contains(line, "coverage:"))
}

func isCommandFailureLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "--- FAIL:") || line == "FAIL" ||
		strings.HasPrefix(line, "FAIL\t") || strings.HasPrefix(line, "panic:") ||
		strings.HasPrefix(line, "fatal error:")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
