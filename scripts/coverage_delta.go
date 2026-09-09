//go:build featuretools

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"math/bits"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	StartCol   int
	EndLine    int
	EndCol     int
	Statements int
	Covered    bool
}

type coverageBlockIdentity struct {
	File  string
	Range string
}

type coverageBlockStructure struct {
	StartLine  int
	StartCol   int
	EndLine    int
	EndCol     int
	Statements int
}

type goCachePaths struct {
	Build   string `json:"GOCACHE"`
	Modules string `json:"GOMODCACHE"`
}

type preparedCoverageRef struct {
	label       string
	ref         string
	worktree    string
	profileRoot string
	home        string
	environment []string
}

type coverageRefResult struct {
	profile coverageProfile
	err     error
}

type coveragePlan struct {
	CoverPackageDirs  []string
	TestPackageDirs   []string
	IntegrationSuites []string
	ImpactedFeature   map[string]bool
	ChangedLines      map[string]map[int]bool
	RelocatedFiles    map[string]string
	GlobalRelevant    bool
}

type scriptCoverageGroup struct {
	Name  string
	Files []string
}

const (
	newFeatureMinimumCoveragePercent   = 95
	changedCodeMinimumCoveragePercent  = 90
	coverageNestedBenchmarkSkipPattern = `^Test(GraderAcceptsReferenceAndReportsMutationEvidence|CodingAgentBenchmarkScriptedGatewayPath|WorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig)$`
	coverageGoTestCount                = 1
	coverageGoTestParallelism          = 1
	coverageGoMaxProcs                 = 2
)

type listedPackage struct {
	ImportPath string
	Dir        string
	RepoDir    string
}

type internalPackageRelocation struct {
	SourceDir      string
	DestinationDir string
	Renames        map[string]string
}

type verifiedInternalPackageRelocation struct {
	ImportOnlyFiles map[string]bool
	RelocatedFiles  map[string]string
}

type sourceEdit struct {
	Start       int
	End         int
	Replacement string
}

type trackedPackageFile struct {
	Mode   string
	Type   string
	Object string
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

func runCoverageDelta(
	root, base, head, tags string,
	forcedPackages []string,
	includeIntegration bool,
) (resultErr error) {
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

	// Keep the physical root short: historical tests create Unix sockets below
	// t.TempDir, and Linux counts the complete pathname against sun_path.
	tmpDir, err := createCoverageTemporaryRoot("")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, removeCoverageTemporaryRoot(tmpDir))
	}()

	ambientEnvironment := os.Environ()
	goCaches, err := resolveGoCachePaths(root, ambientEnvironment)
	if err != nil {
		return err
	}

	refs := make([]preparedCoverageRef, 0, 2)
	for _, candidate := range []struct {
		label string
		ref   string
	}{
		{label: "base", ref: base},
		{label: "head", ref: head},
	} {
		prepared, prepareErr := prepareCoverageRef(
			root,
			tmpDir,
			candidate.label,
			candidate.ref,
			ambientEnvironment,
			goCaches,
		)
		if prepareErr != nil {
			return errors.Join(prepareErr, cleanupCoverageRefs(root, refs))
		}
		refs = append(refs, prepared)
	}

	baseProfile, headProfile, err := runCoveragePair(
		refs[0],
		refs[1],
		func(prepared preparedCoverageRef) (coverageProfile, error) {
			return coverageForPreparedRef(prepared, tags, plan, includeIntegration)
		},
		func(prepared preparedCoverageRef) error {
			return cleanupCoverageRef(root, prepared)
		},
	)
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
		changedCodeStatus(changedCodeCoverage(plan.ChangedLines, headProfile)),
	)
	return nil
}

func createCoverageTemporaryRoot(parent string) (string, error) {
	tmpDir, err := os.MkdirTemp(parent, "pc-")
	if err != nil {
		return "", err
	}
	return tmpDir, nil
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
	relocation, err := verifiedInternalPackageRelocationChanges(root, base, head)
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
		RelocatedFiles:  relocation.RelocatedFiles,
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
		if !relocation.ImportOnlyFiles[path] {
			for _, owner := range codeOwnersForPath(specs, path) {
				plan.ImpactedFeature[owner.SpecRelPath] = true
			}
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

// verifiedInternalPackageRelocationChanges recognizes one deliberately
// narrow refactor: one complete, byte- and mode-identical package tree moved
// from pkg/ to the matching internal/ path, plus consumers changed only to
// import the new path.
// The returned files remain in the ordinary cover and test package sets; only
// their feature-impact fan-out is suppressed. Any other change except modified
// feature specifications disables the exception for the whole comparison.
func verifiedInternalPackageRelocationChanges(
	root, base, head string,
) (verifiedInternalPackageRelocation, error) {
	records, err := changedFileStatusRecords(root, base, head)
	if err != nil {
		return verifiedInternalPackageRelocation{}, err
	}
	for _, record := range records {
		for _, path := range record.Paths {
			if path == "go.mod" || path == "go.sum" {
				return verifiedInternalPackageRelocation{}, nil
			}
		}
	}

	relocations := make(map[string]*internalPackageRelocation)
	relocationRecords := make(map[string]bool)
	for _, record := range records {
		sourceDir, destinationDir, ok := internalPackageRelocationRecord(record)
		if !ok {
			continue
		}
		relocation := relocations[sourceDir]
		if relocation == nil {
			relocation = &internalPackageRelocation{
				SourceDir:      sourceDir,
				DestinationDir: destinationDir,
				Renames:        make(map[string]string),
			}
			relocations[sourceDir] = relocation
		}
		source, destination := record.Paths[0], record.Paths[1]
		relocation.Renames[source] = destination
		relocationRecords[source+"\x00"+destination] = true
	}
	if len(relocations) != 1 {
		return verifiedInternalPackageRelocation{}, nil
	}
	for _, relocation := range relocations {
		for _, record := range records {
			if !internalPackageRelocationMember(record, relocation) {
				continue
			}
			source, destination := record.Paths[0], record.Paths[1]
			relocation.Renames[source] = destination
			relocationRecords[source+"\x00"+destination] = true
		}
	}

	comparisonBase, err := coverageComparisonBase(root, base, head)
	if err != nil {
		return verifiedInternalPackageRelocation{}, err
	}
	baseGoMod, err := gitFileAtRef(root, comparisonBase, "go.mod")
	if err != nil {
		return verifiedInternalPackageRelocation{}, err
	}
	module := modulePathFromGoMod(baseGoMod)
	if module == "" {
		return verifiedInternalPackageRelocation{}, nil
	}

	importReplacements := make(map[string]string)
	for _, relocation := range relocations {
		complete, verifyErr := verifyInternalPackageRelocation(
			root,
			comparisonBase,
			head,
			relocation,
		)
		if verifyErr != nil {
			return verifiedInternalPackageRelocation{}, verifyErr
		}
		if !complete {
			return verifiedInternalPackageRelocation{}, nil
		}
		importReplacements[module+"/"+relocation.SourceDir] =
			module + "/" + relocation.DestinationDir
	}

	importOnlyFiles := make(map[string]bool)
	for _, record := range records {
		if len(record.Paths) == 2 && record.Status == "R100" &&
			relocationRecords[record.Paths[0]+"\x00"+record.Paths[1]] {
			continue
		}
		if !changedStatusTouchesGo(record) {
			if record.Status == "M" && len(record.Paths) == 1 && isFeatureSpecPath(record.Paths[0]) {
				continue
			}
			return verifiedInternalPackageRelocation{}, nil
		}
		if record.Status != "M" || len(record.Paths) != 1 {
			return verifiedInternalPackageRelocation{}, nil
		}
		path := record.Paths[0]
		baseSource, headSource, readErr := gitFilePairAtRefs(root, comparisonBase, head, path)
		if readErr != nil {
			return verifiedInternalPackageRelocation{}, readErr
		}
		if !isGofmtImportOnlyRelocationChange(baseSource, headSource, importReplacements) {
			return verifiedInternalPackageRelocation{}, nil
		}
		importOnlyFiles[path] = true
	}
	result := verifiedInternalPackageRelocation{ImportOnlyFiles: importOnlyFiles}
	for _, relocation := range relocations {
		result.RelocatedFiles = maps.Clone(relocation.Renames)
	}
	return result, nil
}

func coverageComparisonBase(root, base, head string) (string, error) {
	out, err := gitOutput(root, "merge-base", base, head)
	if err != nil {
		return "", fmt.Errorf("git merge-base coverage relocation %s...%s: %w", base, head, err)
	}
	return strings.TrimSpace(out), nil
}

func gitFileAtRef(root, ref, path string) ([]byte, error) {
	out, err := gitOutput(root, "show", ref+":"+path)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, ref, err)
	}
	return []byte(out), nil
}

func gitFilePairAtRefs(root, base, head, path string) ([]byte, []byte, error) {
	baseSource, err := gitFileAtRef(root, base, path)
	if err != nil {
		return nil, nil, err
	}
	headSource, err := gitFileAtRef(root, head, path)
	if err != nil {
		return nil, nil, err
	}
	return baseSource, headSource, nil
}

func modulePathFromGoMod(contents []byte) string {
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "module" {
			continue
		}
		module := fields[1]
		if unquoted, err := strconv.Unquote(module); err == nil {
			module = unquoted
		}
		if module != "" && !strings.ContainsAny(module, "\\\x00") {
			return module
		}
		return ""
	}
	return ""
}

func internalPackageRelocationRecord(record changedFileStatus) (string, string, bool) {
	if record.Status != "R100" || len(record.Paths) != 2 {
		return "", "", false
	}
	source, destination := record.Paths[0], record.Paths[1]
	if !strings.HasSuffix(source, ".go") || !strings.HasSuffix(destination, ".go") ||
		filepath.Base(source) != filepath.Base(destination) {
		return "", "", false
	}
	sourceDir := normalizeRepoPath(filepath.Dir(source))
	destinationDir := normalizeRepoPath(filepath.Dir(destination))
	if !strings.HasPrefix(sourceDir, "pkg/") {
		return "", "", false
	}
	relative := strings.TrimPrefix(sourceDir, "pkg/")
	if relative == "" || destinationDir != "internal/"+relative {
		return "", "", false
	}
	return sourceDir, destinationDir, true
}

func internalPackageRelocationMember(
	record changedFileStatus,
	relocation *internalPackageRelocation,
) bool {
	if record.Status != "R100" || len(record.Paths) != 2 {
		return false
	}
	source, destination := record.Paths[0], record.Paths[1]
	prefix := relocation.SourceDir + "/"
	if !strings.HasPrefix(source, prefix) {
		return false
	}
	relative := strings.TrimPrefix(source, prefix)
	return relative != "" && destination == relocation.DestinationDir+"/"+relative
}

func verifyInternalPackageRelocation(
	root, base, head string,
	relocation *internalPackageRelocation,
) (bool, error) {
	baseSources, err := trackedPackageFiles(root, base, relocation.SourceDir)
	if err != nil {
		return false, err
	}
	baseDestinations, err := trackedPackageFiles(root, base, relocation.DestinationDir)
	if err != nil {
		return false, err
	}
	headSources, err := trackedPackageFiles(root, head, relocation.SourceDir)
	if err != nil {
		return false, err
	}
	headDestinations, err := trackedPackageFiles(root, head, relocation.DestinationDir)
	if err != nil {
		return false, err
	}
	if len(baseSources) == 0 || len(baseDestinations) != 0 || len(headSources) != 0 ||
		len(baseSources) != len(relocation.Renames) ||
		len(headDestinations) != len(relocation.Renames) {
		return false, nil
	}

	hasProductionFile := false
	for source, destination := range relocation.Renames {
		baseFile, baseExists := baseSources[source]
		headFile, headExists := headDestinations[destination]
		if !baseExists || !headExists || baseFile.Type != "blob" || headFile.Type != "blob" ||
			(baseFile.Mode != "100644" && baseFile.Mode != "100755") ||
			baseFile.Mode != headFile.Mode || baseFile.Object != headFile.Object {
			return false, nil
		}
		if isGoProductionCoverageFile(source) && isProductionCodePath(source) {
			hasProductionFile = true
		}
	}
	return hasProductionFile, nil
}

func trackedPackageFiles(root, ref, dir string) (map[string]trackedPackageFile, error) {
	out, err := gitOutput(
		root,
		"--literal-pathspecs",
		"ls-tree",
		"-r",
		"-z",
		ref,
		"--",
		dir,
	)
	if err != nil {
		return nil, fmt.Errorf("list package %s at %s: %w", dir, ref, err)
	}
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\x00") {
		return nil, fmt.Errorf("list package %s at %s: output is not NUL-terminated", dir, ref)
	}
	files := make(map[string]trackedPackageFile)
	for _, record := range strings.Split(out[:len(out)-1], "\x00") {
		metadata, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("list package %s at %s: malformed tree record", dir, ref)
		}
		path = normalizeRepoPath(path)
		if strings.HasPrefix(path, dir+"/") {
			files[path] = trackedPackageFile{
				Mode:   fields[0],
				Type:   fields[1],
				Object: fields[2],
			}
		}
	}
	return files, nil
}

func changedStatusTouchesGo(record changedFileStatus) bool {
	for _, path := range record.Paths {
		if strings.HasSuffix(path, ".go") {
			return true
		}
	}
	return false
}

func isGofmtImportOnlyRelocationChange(
	baseSource, headSource []byte,
	importReplacements map[string]string,
) bool {
	if bytes.Equal(baseSource, headSource) {
		return false
	}
	formattedBase, err := format.Source(baseSource)
	if err != nil || !bytes.Equal(formattedBase, baseSource) {
		return false
	}
	formattedHead, err := format.Source(headSource)
	if err != nil || !bytes.Equal(formattedHead, headSource) {
		return false
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "consumer.go", baseSource, parser.ParseComments)
	if err != nil {
		return false
	}
	var edits []sourceEdit
	for _, importSpec := range file.Imports {
		path, _ := strconv.Unquote(importSpec.Path.Value)
		replacement, ok := importReplacements[path]
		if !ok {
			continue
		}
		if importSpec.Path.Value != strconv.Quote(path) {
			return false
		}
		start := fileSet.PositionFor(importSpec.Path.Pos(), false).Offset
		end := fileSet.PositionFor(importSpec.Path.End(), false).Offset
		edits = append(edits, sourceEdit{
			Start:       start,
			End:         end,
			Replacement: strconv.Quote(replacement),
		})
	}
	if len(edits) == 0 {
		return false
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	var rewritten bytes.Buffer
	previousEnd := 0
	for _, edit := range edits {
		rewritten.Write(baseSource[previousEnd:edit.Start])
		rewritten.WriteString(edit.Replacement)
		previousEnd = edit.End
	}
	rewritten.Write(baseSource[previousEnd:])
	expected, err := format.Source(rewritten.Bytes())
	return err == nil && bytes.Equal(expected, headSource)
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
		strings.HasPrefix(path, "internal/") ||
		strings.HasPrefix(path, "pkg/") ||
		strings.HasPrefix(path, "scripts/") ||
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

func prepareCoverageRef(
	root, tmpDir, label, ref string,
	ambientEnvironment []string,
	goCaches goCachePaths,
) (preparedCoverageRef, error) {
	sha, err := resolveGitRef(root, ref)
	if err != nil {
		return preparedCoverageRef{}, err
	}
	worktree := filepath.Join(tmpDir, label)
	if err := gitRun(root, "worktree", "add", "--detach", "--force", worktree, sha); err != nil {
		return preparedCoverageRef{}, fmt.Errorf("create %s worktree for %s: %w", label, ref, err)
	}

	coverageHome := filepath.Join(tmpDir, label+"-picoclaw-home")
	return preparedCoverageRef{
		label:       label,
		ref:         ref,
		worktree:    worktree,
		profileRoot: tmpDir,
		home:        coverageHome,
		environment: coverageEnvironment(ambientEnvironment, coverageHome, goCaches),
	}, nil
}

func cleanupCoverageRefs(root string, refs []preparedCoverageRef) error {
	cleanupErrors := make([]error, 0, len(refs))
	for _, prepared := range refs {
		cleanupErrors = append(cleanupErrors, cleanupCoverageRef(root, prepared))
	}
	return errors.Join(cleanupErrors...)
}

func cleanupCoverageRef(root string, prepared preparedCoverageRef) error {
	permissionErr := makeCoverageWorktreeRemovable(prepared.worktree)
	if permissionErr != nil {
		permissionErr = fmt.Errorf(
			"prepare %s worktree cleanup for %s: %w",
			prepared.label,
			prepared.ref,
			permissionErr,
		)
	}
	removeErr := gitRun(root, "worktree", "remove", "--force", prepared.worktree)
	if removeErr != nil {
		removeErr = fmt.Errorf("remove %s worktree for %s: %w", prepared.label, prepared.ref, removeErr)
	}
	return errors.Join(permissionErr, removeErr)
}

func makeCoverageWorktreeRemovable(root string) error {
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o700)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeCoverageTemporaryRoot(root string) error {
	permissionErr := makeCoverageWorktreeRemovable(root)
	removeErr := os.RemoveAll(root)
	return errors.Join(permissionErr, removeErr)
}

func runCoveragePair(
	base, head preparedCoverageRef,
	collect func(preparedCoverageRef) (coverageProfile, error),
	cleanup func(preparedCoverageRef) error,
) (coverageProfile, coverageProfile, error) {
	refs := [...]preparedCoverageRef{base, head}
	results := [len(refs)]coverageRefResult{}
	var wait sync.WaitGroup
	wait.Add(len(refs))
	for index, prepared := range refs {
		go func() {
			defer wait.Done()
			results[index].profile, results[index].err = collect(prepared)
		}()
	}
	wait.Wait()

	// Git worktree administration is deliberately serial. More importantly,
	// neither worktree can disappear while the peer collector still has live
	// processes or profiles below it.
	cleanupErrors := make([]error, 0, len(refs))
	for _, prepared := range refs {
		cleanupErrors = append(cleanupErrors, cleanup(prepared))
	}

	// Indexed results preserve base-before-head diagnostics even when head
	// happens to fail first. Join keeps both failures visible without cancelling
	// the peer collector before it can finish and release its resources.
	return results[0].profile, results[1].profile, errors.Join(
		results[0].err,
		results[1].err,
		errors.Join(cleanupErrors...),
	)
}

func coverageForPreparedRef(
	prepared preparedCoverageRef,
	tags string,
	plan coveragePlan,
	includeIntegration bool,
) (coverageProfile, error) {
	label := prepared.label
	ref := prepared.ref
	worktree := prepared.worktree
	tmpDir := prepared.profileRoot
	coverageHome := prepared.home
	environment := prepared.environment

	if err := prepareCoverageStorage(coverageHome); err != nil {
		return coverageProfile{}, fmt.Errorf("create %s coverage home: %w", label, err)
	}
	if err := runGoGenerate(worktree, label, ref, environment); err != nil {
		return coverageProfile{}, err
	}
	if err := buildCoverageTestBinary(worktree, label, ref, tags, environment); err != nil {
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
	collectScripts := coveragePlanIncludesDirectory(plan.CoverPackageDirs, "scripts")
	if len(testImports) == 0 && !collectScripts {
		return emptyCoverageProfile(), nil
	}

	unitEnvironment := coverageFallbackHomeEnvironment(environment)
	profile := emptyCoverageProfile()
	if len(testImports) > 0 {
		profilePath := filepath.Join(tmpDir, label+".cover.out")
		profile, err = runGoCoverage(
			worktree,
			label,
			ref,
			tags,
			profilePath,
			coverImports,
			testImports,
			unitEnvironment,
		)
		if err != nil {
			return coverageProfile{}, err
		}
	}
	if collectScripts {
		scriptProfile, scriptErr := runScriptCoverage(
			worktree,
			label,
			ref,
			tags,
			filepath.Join(tmpDir, label+"-script-coverage"),
			unitEnvironment,
		)
		if scriptErr != nil {
			return coverageProfile{}, scriptErr
		}
		profile = mergeCoverageProfiles(profile, scriptProfile)
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

func coveragePlanIncludesDirectory(dirs []string, wanted string) bool {
	wanted = normalizeRepoPath(wanted)
	for _, dir := range dirs {
		if normalizeRepoPath(dir) == wanted {
			return true
		}
	}
	return false
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
		"-count",
		strconv.Itoa(coverageGoTestCount),
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
	// The guard tests detached base and head worktrees. A failure while testing
	// the immutable base cannot be caused by the proposed change, so retry the
	// base command once. A deterministic base failure still fails on the second
	// attempt, while head failures are always final.
	out, err, retried := runCoverageCommandWithBaselineRetry(label, run)
	if retried {
		fmt.Fprintf(
			os.Stderr,
			"coverage delta: retried %s (%s) after baseline coverage failure\n",
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
	if err == nil || label != "base" {
		return out, err, false
	}
	out, err = run()
	return out, err, true
}

// runScriptCoverage collects top-level Go script programs one at a time. The
// scripts directory intentionally contains several package-main entrypoints,
// so package-pattern coverage would either omit featuretools-tagged sources or
// fail with duplicate main declarations. Explicit files match the supported
// Makefile test invocations while keeping each program in its own test binary.
func runScriptCoverage(
	worktree, label, ref, tags, profileDir string,
	environment []string,
) (coverageProfile, error) {
	groups, err := scriptCoverageGroups(worktree)
	if err != nil {
		return coverageProfile{}, fmt.Errorf(
			"script coverage for %s (%s): %w",
			label,
			ref,
			err,
		)
	}
	if len(groups) == 0 {
		return emptyCoverageProfile(), nil
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return coverageProfile{}, fmt.Errorf(
			"create script coverage directory for %s (%s): %w",
			label,
			ref,
			err,
		)
	}
	module, err := modulePath(worktree)
	if err != nil {
		return coverageProfile{}, err
	}

	profile := emptyCoverageProfile()
	for _, group := range groups {
		profilePath := filepath.Join(profileDir, group.Name+".cover.out")
		args := []string{
			"test",
			"-buildvcs=false",
			"-count",
			strconv.Itoa(coverageGoTestCount),
			"-tags",
			scriptCoverageBuildTags(tags),
			"-covermode=atomic",
			"-coverprofile",
			profilePath,
		}
		for _, file := range group.Files {
			args = append(args, "./"+filepath.ToSlash(file))
		}
		run := func() ([]byte, error) {
			cmd := exec.Command("go", args...)
			cmd.Dir = worktree
			cmd.Env = append([]string(nil), environment...)
			return cmd.CombinedOutput()
		}
		out, runErr, retried := runCoverageCommandWithBaselineRetry(label, run)
		if retried {
			fmt.Fprintf(
				os.Stderr,
				"coverage delta: retried %s script group %s (%s) after baseline coverage failure\n",
				label,
				group.Name,
				ref,
			)
		}
		if runErr != nil {
			return coverageProfile{}, fmt.Errorf(
				"script coverage for %s group %s (%s): %w\n%s",
				label,
				group.Name,
				ref,
				runErr,
				trimCommandOutput(out),
			)
		}
		next, parseErr := parseCoverageProfile(worktree, module, profilePath)
		if parseErr != nil {
			return coverageProfile{}, fmt.Errorf(
				"parse %s script coverage group %s: %w",
				label,
				group.Name,
				parseErr,
			)
		}
		profile = mergeCoverageProfiles(profile, next)
	}
	return profile, nil
}

func scriptCoverageBuildTags(tags string) string {
	tags = strings.Trim(strings.TrimSpace(tags), ",")
	if tags == "" {
		return "featuretools"
	}
	for _, tag := range strings.FieldsFunc(tags, func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t'
	}) {
		if tag == "featuretools" {
			return tags
		}
	}
	return "featuretools," + tags
}

// scriptCoverageGroups creates one group per production Go file directly under
// scripts. Shared featuretools helpers are included in every group, but never
// become a second main entrypoint. The two special test mappings are the exact
// explicit-file commands used by make test-featuretools; future programs pick
// up a same-basename test automatically.
func scriptCoverageGroups(worktree string) ([]scriptCoverageGroup, error) {
	scriptsDir := filepath.Join(worktree, "scripts")
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	shared := "scripts/featuretools_lib.go"
	sharedAvailable, err := scriptCoverageFileAvailable(worktree, shared)
	if err != nil {
		return nil, err
	}

	var groups []scriptCoverageGroup
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || name == filepath.Base(shared) {
			continue
		}
		anchor := filepath.ToSlash(filepath.Join("scripts", name))
		available, inspectErr := scriptCoverageFileAvailable(worktree, anchor)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if !available {
			continue
		}
		files := []string{anchor}
		if sharedAvailable {
			files = append(files, shared)
		}
		for _, testFile := range scriptCoverageTestFiles(name) {
			available, inspectErr = scriptCoverageFileAvailable(worktree, testFile)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if available {
				files = append(files, testFile)
			}
		}
		groups = append(groups, scriptCoverageGroup{
			Name:  strings.TrimSuffix(name, ".go"),
			Files: files,
		})
	}
	if len(groups) == 0 && sharedAvailable {
		groups = append(groups, scriptCoverageGroup{
			Name: "featuretools_lib", Files: []string{shared},
		})
	}
	return groups, nil
}

func scriptCoverageTestFiles(program string) []string {
	switch program {
	case "coverage_delta.go":
		return []string{"scripts/coverage_delta_test.go"}
	case "feature_delta_guard.go":
		return []string{"scripts/featuretools_lib_test.go"}
	default:
		return []string{
			filepath.ToSlash(filepath.Join(
				"scripts",
				strings.TrimSuffix(program, ".go")+"_test.go",
			)),
		}
	}
}

func scriptCoverageFileAvailable(worktree, relative string) (bool, error) {
	path := filepath.Join(worktree, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect script coverage file %s: %w", relative, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("script coverage file %s is not a regular file", relative)
	}
	return true, nil
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
		"INTEGRATION_COMPOSE_PROJECT_NAMESPACE="+filepath.Base(filepath.Dir(worktree))+"-"+label,
		"INTEGRATION_GOMAXPROCS="+strconv.Itoa(coverageGoMaxProcs),
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
			strings.EqualFold(name, "GOTOOLCHAIN") ||
			strings.EqualFold(name, "GOMAXPROCS")) {
			continue
		}
		environment = append(environment, entry)
	}
	picoHome := filepath.Join(home, ".picoclaw")
	temporaryDirectory := coverageTemporaryDirectory(home)
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
		"TMPDIR="+temporaryDirectory,
		"TEMP="+temporaryDirectory,
		"TMP="+temporaryDirectory,
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
		"GOMAXPROCS="+strconv.Itoa(coverageGoMaxProcs),
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

func coverageTemporaryDirectory(home string) string {
	name := strings.TrimSuffix(filepath.Base(home), "-picoclaw-home") + "-tmp"
	return filepath.Join(filepath.Dir(home), name)
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
		coverageTemporaryDirectory(home),
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
	// Explicit-file coverage profiles use absolute source paths. Split at the
	// final colon so a Windows drive prefix remains part of the filename.
	colon := strings.LastIndexByte(line, ':')
	if colon < 0 {
		return coverageBlock{}, fmt.Errorf("invalid coverage line %q", line)
	}
	filePath := coverageFileToRepoPath(root, modulePath, line[:colon])
	fields := strings.Fields(line[colon+1:])
	if len(fields) != 3 {
		return coverageBlock{}, fmt.Errorf("invalid coverage fields %q", line)
	}
	startLine, startCol, endLine, endCol, err := coverageRange(fields[0])
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
		StartCol:   startCol,
		EndLine:    endLine,
		EndCol:     endCol,
		Statements: statements,
		Covered:    count > 0,
	}, nil
}

func coverageRange(value string) (int, int, int, int, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("expected start,end")
	}
	startLine, startCol, err := coveragePoint(parts[0])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	endLine, endCol, err := coveragePoint(parts[1])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return startLine, startCol, endLine, endCol, nil
}

func coveragePoint(value string) (int, int, error) {
	lineText, colText, ok := strings.Cut(value, ".")
	if !ok {
		return 0, 0, fmt.Errorf("expected line.column")
	}
	line, lineErr := strconv.Atoi(lineText)
	column, colErr := strconv.Atoi(colText)
	if lineErr != nil || colErr != nil || line < 1 || column < 1 {
		return 0, 0, fmt.Errorf("expected positive line.column")
	}
	return line, column, nil
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

func relocationCoverageStructureMatches(
	baseProfile, headProfile coverageProfile,
	relocatedFiles map[string]string,
) error {
	canonicalRelocations := make(map[string]string, len(relocatedFiles))
	destinations := make(map[string]string, len(relocatedFiles))
	for source, destination := range relocatedFiles {
		canonicalSource := normalizeRepoPath(source)
		canonicalDestination := normalizeRepoPath(destination)
		if source == "" || destination == "" || strings.ContainsRune(source, '\x00') ||
			strings.ContainsRune(destination, '\x00') || source != canonicalSource ||
			destination != canonicalDestination || source == destination {
			return fmt.Errorf("invalid relocation path %q -> %q", source, destination)
		}
		if previous, exists := destinations[destination]; exists && previous != source {
			return fmt.Errorf(
				"relocation destinations collide at %q for %q and %q",
				destination,
				previous,
				source,
			)
		}
		canonicalRelocations[source] = destination
		destinations[destination] = source
	}
	if len(canonicalRelocations) == 0 {
		return errors.New("relocation map is empty")
	}
	for source, destination := range canonicalRelocations {
		if _, overlaps := canonicalRelocations[destination]; overlaps {
			return fmt.Errorf("relocation destination %q is also a source", destination)
		}
		if _, represented := baseProfile.Blocks[destination]; represented {
			return fmt.Errorf(
				"relocation %q -> %q collides with a base coverage file",
				source,
				destination,
			)
		}
	}

	baseStructure, err := coverageStructure(baseProfile, canonicalRelocations)
	if err != nil {
		return fmt.Errorf("base profile: %w", err)
	}
	headStructure, err := coverageStructure(headProfile, nil)
	if err != nil {
		return fmt.Errorf("head profile: %w", err)
	}
	if len(baseStructure) != len(headStructure) {
		return fmt.Errorf(
			"block count changed from %d to %d",
			len(baseStructure),
			len(headStructure),
		)
	}
	for identity, baseBlock := range baseStructure {
		headBlock, exists := headStructure[identity]
		if !exists || headBlock != baseBlock {
			return fmt.Errorf("block structure differs at %s:%s", identity.File, identity.Range)
		}
	}
	return nil
}

func coverageStructure(
	profile coverageProfile,
	pathReplacements map[string]string,
) (map[coverageBlockIdentity]coverageBlockStructure, error) {
	structure := make(map[coverageBlockIdentity]coverageBlockStructure)
	statements := 0
	for file, blocks := range profile.Blocks {
		normalizedFile := file
		if replacement, ok := pathReplacements[file]; ok {
			normalizedFile = replacement
		}
		for rangeKey, block := range blocks {
			if block.File != file || block.Range != rangeKey {
				return nil, fmt.Errorf("block map identity differs at %s:%s", file, rangeKey)
			}
			startLine, startCol, endLine, endCol, rangeErr := coverageRange(block.Range)
			if rangeErr != nil || block.StartLine != startLine || block.StartCol != startCol ||
				block.EndLine != endLine || block.EndCol != endCol || block.Statements < 0 {
				return nil, fmt.Errorf("block metadata is invalid at %s:%s", file, rangeKey)
			}
			identity := coverageBlockIdentity{File: normalizedFile, Range: block.Range}
			structure[identity] = coverageBlockStructure{
				StartLine:  block.StartLine,
				StartCol:   block.StartCol,
				EndLine:    block.EndLine,
				EndCol:     block.EndCol,
				Statements: block.Statements,
			}
			statements += block.Statements
		}
	}
	if statements != profile.Global.TotalStatements {
		return nil, fmt.Errorf(
			"block statements %d do not match global total %d",
			statements,
			profile.Global.TotalStatements,
		)
	}
	return structure, nil
}

func compareCoverage(
	specs []featureSpecMetadata,
	plan coveragePlan,
	baseProfile, headProfile coverageProfile,
) []string {
	var failures []string
	changedSummary := changedCodeCoverage(plan.ChangedLines, headProfile)
	var relocationStructureErr error
	if len(plan.RelocatedFiles) > 0 {
		relocationStructureErr = relocationCoverageStructureMatches(
			baseProfile,
			headProfile,
			plan.RelocatedFiles,
		)
	}
	waiveGlobalRegression := len(plan.RelocatedFiles) > 0 &&
		relocationStructureErr == nil && changedSummary.TotalStatements == 0
	if baseProfile.Global.TotalStatements == 0 && headProfile.Global.TotalStatements > 0 &&
		!coverageAtLeastPercent(headProfile.Global, newFeatureMinimumCoveragePercent) {
		failures = append(failures, fmt.Sprintf(
			"scoped new Go coverage is below %d%%: %s",
			newFeatureMinimumCoveragePercent,
			formatCoverage(headProfile.Global),
		))
	} else if !waiveGlobalRegression && baseProfile.Global.TotalStatements > 0 &&
		summaryRegressed(baseProfile.Global, headProfile.Global) {
		failures = append(failures, fmt.Sprintf(
			"scoped Go coverage regressed: uncovered statement debt %d -> %d and coverage %s -> %s",
			uncoveredStatements(baseProfile.Global),
			uncoveredStatements(headProfile.Global),
			formatCoverage(baseProfile.Global),
			formatCoverage(headProfile.Global),
		))
	}
	if relocationStructureErr != nil {
		failures = append(failures, fmt.Sprintf(
			"verified internal package relocation coverage structure mismatch: %v",
			relocationStructureErr,
		))
	}
	if changedSummary.TotalStatements > 0 &&
		!coverageAtLeastPercent(changedSummary, changedCodeMinimumCoveragePercent) {
		failures = append(failures, fmt.Sprintf(
			"changed production Go coverage is below %d%%: %s",
			changedCodeMinimumCoveragePercent,
			formatCoverage(changedSummary),
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
		if baseSummary.TotalStatements == 0 {
			if !coverageAtLeastPercent(headSummary, newFeatureMinimumCoveragePercent) {
				failures = append(failures, fmt.Sprintf(
					"%s new Go feature coverage is below %d%%: %s",
					spec.RelPath,
					newFeatureMinimumCoveragePercent,
					formatCoverage(headSummary),
				))
			}
			continue
		}
		if summaryRegressed(baseSummary, headSummary) {
			failures = append(failures, fmt.Sprintf(
				"%s Go coverage regressed: uncovered statement debt %d -> %d and coverage %s -> %s",
				spec.RelPath,
				uncoveredStatements(baseSummary),
				uncoveredStatements(headSummary),
				formatCoverage(baseSummary),
				formatCoverage(headSummary),
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

func changedCodeCoverage(
	changedLines map[string]map[int]bool,
	profile coverageProfile,
) coverageSummary {
	changedBlocks := make(map[coverageBlockIdentity]coverageBlock)
	for file, lines := range changedLines {
		if !isGoProductionCoverageFile(file) || !isProductionCodePath(file) {
			continue
		}
		for _, block := range profile.Blocks[file] {
			if !blockTouchesChangedLine(block, lines) {
				continue
			}
			identity := coverageBlockIdentity{File: file, Range: block.Range}
			if existing, ok := changedBlocks[identity]; ok {
				existing.Covered = existing.Covered || block.Covered
				changedBlocks[identity] = existing
				continue
			}
			block.File = file
			changedBlocks[identity] = block
		}
	}
	var summary coverageSummary
	for _, block := range changedBlocks {
		summary.TotalStatements += block.Statements
		if block.Covered {
			summary.CoveredStatements += block.Statements
		}
	}
	return summary
}

func blockTouchesChangedLine(block coverageBlock, lines map[int]bool) bool {
	for line := range lines {
		endsAfterLineStart := line < block.EndLine ||
			line == block.EndLine && (block.EndCol == 0 || block.EndCol > 1)
		if line >= block.StartLine && endsAfterLineStart {
			return true
		}
	}
	return false
}

func changedCodeStatus(summary coverageSummary) string {
	if summary.TotalStatements == 0 {
		return "no changed executable Go statements"
	}
	return fmt.Sprintf("changed executable Go coverage %s", formatCoverage(summary))
}

func changedGoLines(root, base, head string) (map[string]map[int]bool, error) {
	changed, err := changedFileStatusRecords(root, base, head)
	if err != nil {
		return nil, err
	}
	mergeBaseOutput, err := gitOutput(root, "merge-base", base, head)
	if err != nil {
		return nil, fmt.Errorf("git merge-base changed lines %s...%s: %w", base, head, err)
	}
	mergeBase := strings.TrimSpace(mergeBaseOutput)
	if mergeBase == "" {
		return nil, fmt.Errorf("git merge-base changed lines %s...%s returned no commit", base, head)
	}

	result := make(map[string]map[int]bool)
	for _, change := range changed {
		destination := change.Paths[len(change.Paths)-1]
		if change.Kind == 'D' || !strings.HasSuffix(destination, ".go") {
			continue
		}
		var out string
		var diffErr error
		if change.Kind == 'A' || change.Kind == 'C' {
			out, diffErr = gitOutput(
				root,
				"--literal-pathspecs",
				"diff",
				"--unified=0",
				"--no-ext-diff",
				mergeBase,
				head,
				"--",
				destination,
			)
		} else {
			out, diffErr = gitOutput(
				root,
				"diff",
				"--unified=0",
				"--no-ext-diff",
				mergeBase+":"+change.Paths[0],
				head+":"+destination,
			)
		}
		if diffErr != nil {
			return nil, fmt.Errorf(
				"git diff changed lines for %q %s...%s: %w",
				destination,
				base,
				head,
				diffErr,
			)
		}
		lines, parseErr := parseAddedDiffLines(out)
		if parseErr != nil {
			return nil, fmt.Errorf("parse changed lines for %q: %w", destination, parseErr)
		}
		if len(lines) != 0 {
			result[destination] = lines
		}
	}
	return result, nil
}

func parseAddedDiffLines(out string) (map[int]bool, error) {
	lines := make(map[int]bool)
	inHunk := false
	newLine := 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "@@ ") {
			start, err := parseDiffNewStart(line)
			if err != nil {
				return nil, err
			}
			newLine = start
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			lines[newLine] = true
			newLine++
		case strings.HasPrefix(line, "-"):
		case strings.HasPrefix(line, " "):
			newLine++
		}
	}
	return lines, nil
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
	return uncoveredStatements(head) > uncoveredStatements(base) &&
		coverageRatioLess(head, base)
}

func coverageAtLeastPercent(summary coverageSummary, minimum int) bool {
	return !coverageRatioLess(summary, coverageSummary{
		CoveredStatements: minimum,
		TotalStatements:   100,
	})
}

func coverageRatioLess(left, right coverageSummary) bool {
	leftCovered, leftTotal := exactCoverageRatio(left)
	rightCovered, rightTotal := exactCoverageRatio(right)
	leftHigh, leftLow := bits.Mul64(leftCovered, rightTotal)
	rightHigh, rightLow := bits.Mul64(rightCovered, leftTotal)
	if leftHigh != rightHigh {
		return leftHigh < rightHigh
	}
	return leftLow < rightLow
}

func exactCoverageRatio(summary coverageSummary) (uint64, uint64) {
	if summary.TotalStatements == 0 {
		return 1, 1
	}
	return uint64(summary.CoveredStatements), uint64(summary.TotalStatements)
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
