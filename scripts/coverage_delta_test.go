//go:build featuretools

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChangedGoLinesHandlesOddPathsC100AndFeedsChangedCoverage(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Win32 filenames cannot contain the odd-path fixture characters")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	root := t.TempDir()
	runCoverageDeltaTestGit(t, root, "init", "--quiet")
	runCoverageDeltaTestGit(t, root, "config", "user.email", "coverage-delta@example.invalid")
	runCoverageDeltaTestGit(t, root, "config", "user.name", "Coverage Delta Test")
	runCoverageDeltaTestGit(t, root, "config", "commit.gpgSign", "false")
	hooks := filepath.Join(root, "disabled-hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	runCoverageDeltaTestGit(t, root, "config", "core.hooksPath", hooks)

	newlineFile := "pkg/odd/line\nbreak.go"
	quotedOldFile := `pkg/odd/quote"name.go`
	quotedNewFile := `pkg/odd/renamed"name.go`
	addedFile := "pkg/odd/added.go"
	copySourceFile := "pkg/odd/copy-source.go"
	copyDestinationFile := "pkg/odd/copy-destination.go"
	deletedFile := "pkg/odd/deleted.go"
	const addedSource = `package odd

var AddedValues = []string{
	"unique-alpha-value",
	"unique-bravo-value",
	"unique-charlie-value",
	"unique-delta-value",
	"unique-echo-value",
	"unique-foxtrot-value",
}
`
	const copySourceBase = `package odd

var CopyValues = []string{
	"copy-alpha-value",
	"copy-bravo-value",
	"copy-charlie-value",
	"copy-delta-value",
	"copy-echo-value",
	"copy-foxtrot-value",
}
`
	const copySourceHead = `package odd

var CopyValues = []string{
	"copy-alpha-value",
	"copy-bravo-value-modified",
	"copy-charlie-value",
	"copy-delta-value",
	"copy-echo-value",
	"copy-foxtrot-value",
}
`
	for path, source := range map[string]string{
		newlineFile:    "package odd\n\nfunc Newline() int {\n\treturn 1\n}\n",
		quotedOldFile:  "package odd\n\nfunc Quoted() int {\n\treturn 1\n}\n",
		copySourceFile: copySourceBase,
		deletedFile:    "package odd\n\nfunc Deleted() int {\n\treturn 1\n}\n",
	} {
		writeCoverageDeltaTestFile(t, root, path, source)
	}
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD"))

	writeCoverageDeltaTestFile(
		t,
		root,
		newlineFile,
		"package odd\n\nfunc Newline() int {\n\treturn 2\n}\n",
	)
	if err := os.Rename(filepath.Join(root, quotedOldFile), filepath.Join(root, quotedNewFile)); err != nil {
		t.Fatal(err)
	}
	writeCoverageDeltaTestFile(
		t,
		root,
		quotedNewFile,
		"package odd\n\nfunc Quoted() int {\n\treturn 2\n}\n",
	)
	writeCoverageDeltaTestFile(
		t,
		root,
		addedFile,
		addedSource,
	)
	writeCoverageDeltaTestFile(t, root, copySourceFile, copySourceHead)
	writeCoverageDeltaTestFile(t, root, copyDestinationFile, copySourceBase)
	if err := os.Remove(filepath.Join(root, deletedFile)); err != nil {
		t.Fatal(err)
	}
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "head")
	head := strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD"))
	statusOutput := runCoverageDeltaTestGit(
		t,
		root,
		"diff",
		"--name-status",
		"-z",
		"--find-renames",
		"--find-copies",
		"--diff-filter=ACMRTD",
		base+"..."+head,
	)
	wantCopyRecord := "C100\x00" + copySourceFile + "\x00" + copyDestinationFile + "\x00"
	if !strings.Contains(statusOutput, wantCopyRecord) {
		t.Fatalf("name-status output did not contain %q: %q", wantCopyRecord, statusOutput)
	}
	records, err := changedFileStatusRecords(root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	foundCopy := false
	for _, record := range records {
		if record.Kind != 'C' || record.Paths[len(record.Paths)-1] != copyDestinationFile {
			continue
		}
		foundCopy = true
		if want := []string{copySourceFile, copyDestinationFile}; !reflect.DeepEqual(record.Paths, want) {
			t.Fatalf("copy record paths = %#v, want %#v", record.Paths, want)
		}
	}
	if !foundCopy {
		t.Fatalf("changed file records did not contain C100 destination %q: %#v", copyDestinationFile, records)
	}
	files, err := changedFiles(root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, copyDestinationFile) {
		t.Fatalf("flattened copy paths = %#v", files)
	}

	changed, err := changedGoLines(root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{newlineFile, quotedNewFile} {
		if want := map[int]bool{4: true}; !reflect.DeepEqual(changed[file], want) {
			t.Errorf("changed lines for %q = %#v, want %#v", file, changed[file], want)
		}
	}
	wantAdded := make(map[int]bool)
	for line := 1; line <= strings.Count(addedSource, "\n"); line++ {
		wantAdded[line] = true
	}
	if !reflect.DeepEqual(changed[addedFile], wantAdded) {
		t.Errorf("added-file changed lines = %#v, want %#v", changed[addedFile], wantAdded)
	}
	wantCopy := make(map[int]bool)
	for line := 1; line <= strings.Count(copySourceBase, "\n"); line++ {
		wantCopy[line] = true
	}
	if !reflect.DeepEqual(changed[copyDestinationFile], wantCopy) {
		t.Errorf("copied-file changed lines = %#v, want %#v", changed[copyDestinationFile], wantCopy)
	}
	for _, file := range []string{quotedOldFile, deletedFile} {
		if len(changed[file]) != 0 {
			t.Errorf("deleted-side changed lines for %q = %#v, want none", file, changed[file])
		}
	}

	profile := coverageProfile{Blocks: map[string]map[string]coverageBlock{
		newlineFile: {
			"4.2,4.10": {
				File: newlineFile, Range: "4.2,4.10", StartLine: 4, StartCol: 2,
				EndLine: 4, EndCol: 10, Statements: 2, Covered: true,
			},
		},
		quotedNewFile: {
			"4.2,4.10": {
				File: quotedNewFile, Range: "4.2,4.10", StartLine: 4, StartCol: 2,
				EndLine: 4, EndCol: 10, Statements: 1,
			},
		},
		addedFile: {
			"4.2,4.10": {
				File: addedFile, Range: "4.2,4.10", StartLine: 4, StartCol: 2,
				EndLine: 4, EndCol: 10, Statements: 1, Covered: true,
			},
		},
		copyDestinationFile: {
			"4.2,4.10": {
				File: copyDestinationFile, Range: "4.2,4.10", StartLine: 4, StartCol: 2,
				EndLine: 4, EndCol: 10, Statements: 2, Covered: true,
			},
		},
	}}
	if got := changedCodeCoverage(changed, profile); got != (coverageSummary{5, 6}) {
		t.Fatalf("odd-path changed-code coverage = %+v, want 5/6", got)
	}
}

func writeCoverageDeltaTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCoverageDeltaTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestCoverageNestedBenchmarkSkipPatternIsExact(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(coverageNestedBenchmarkSkipPattern)
	for _, name := range []string{
		"TestGraderAcceptsReferenceAndReportsMutationEvidence",
		"TestCodingAgentBenchmarkScriptedGatewayPath",
		"TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig",
	} {
		if !pattern.MatchString(name) {
			t.Fatalf("coverage skip pattern omitted %q", name)
		}
	}
	for _, name := range []string{
		"TestGraderRejectsOutsideOutput",
		"TestCodingAgentBenchmarkLiveOptIn",
		"PrefixTestCodingAgentBenchmarkScriptedGatewayPath",
		"TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfigExtra",
	} {
		if pattern.MatchString(name) {
			t.Fatalf("coverage skip pattern was too broad for %q", name)
		}
	}
}

func TestCoverageGoTestParallelismIsBounded(t *testing.T) {
	t.Parallel()
	if coverageGoTestParallelism != 1 {
		t.Fatalf("coverage Go test parallelism = %d, want 1", coverageGoTestParallelism)
	}
}

func TestCoverageIntegrationSuitesAllowHeadOnlyAddition(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	head := filepath.Join(root, "head")
	for _, path := range []string{
		filepath.Join(base, "integration", "suites", "existing"),
		filepath.Join(head, "integration", "suites", "existing"),
		filepath.Join(head, "integration", "suites", "storage-json"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	planned := []string{"existing", "storage-json"}

	baseSuites, err := coverageIntegrationSuitesForRef(base, "base", "base-ref", planned)
	if err != nil || !reflect.DeepEqual(baseSuites, []string{"existing"}) {
		t.Fatalf("base suites = %#v, %v", baseSuites, err)
	}
	headSuites, err := coverageIntegrationSuitesForRef(head, "head", "head-ref", planned)
	if err != nil || !reflect.DeepEqual(headSuites, planned) {
		t.Fatalf("head suites = %#v, %v", headSuites, err)
	}

	if err := os.Remove(filepath.Join(head, "integration", "suites", "storage-json")); err != nil {
		t.Fatal(err)
	}
	if suites, err := coverageIntegrationSuitesForRef(head, "head", "head-ref", planned); err == nil ||
		suites != nil || !strings.Contains(err.Error(), "planned suite storage-json is missing") {
		t.Fatalf("missing head suite = %#v, %v", suites, err)
	}

	unsafe := filepath.Join(base, "integration", "suites", "storage-json")
	if err := os.WriteFile(unsafe, []byte("not a suite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if suites, err := coverageIntegrationSuitesForRef(base, "base", "base-ref", planned); err == nil ||
		suites != nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("unsafe base suite = %#v, %v", suites, err)
	}

	for _, invalid := range []string{"", ".", "..", "../escape", `child\escape`, " bad"} {
		if suites, err := coverageIntegrationSuitesForRef(
			base,
			"base",
			"base-ref",
			[]string{invalid},
		); err == nil || suites != nil || !strings.Contains(err.Error(), "identity is invalid") {
			t.Fatalf("invalid suite identity %q = %#v, %v", invalid, suites, err)
		}
	}
}

func TestCoverageRegressionUsesDebtAndExactPercentage(t *testing.T) {
	tests := []struct {
		name string
		base coverageSummary
		head coverageSummary
		want bool
	}{
		{
			name: "covered code deletion with unchanged debt",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 70, TotalStatements: 90},
			want: false,
		},
		{
			name: "increased debt and percentage regression",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 80, TotalStatements: 101},
			want: true,
		},
		{
			name: "increased debt with stable percentage",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 160, TotalStatements: 200},
			want: false,
		},
		{
			name: "increased debt with improved percentage",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 162, TotalStatements: 200},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := summaryRegressed(test.base, test.head); got != test.want {
				t.Fatalf("summaryRegressed(%+v, %+v) = %t, want %t", test.base, test.head, got, test.want)
			}
		})
	}
}

func TestCoverageMinimumsUseExactIntegerRatios(t *testing.T) {
	tests := []struct {
		name    string
		summary coverageSummary
		minimum int
		want    bool
	}{
		{
			name:    "new feature exactly ninety five percent",
			summary: coverageSummary{CoveredStatements: 95, TotalStatements: 100},
			minimum: newFeatureMinimumCoveragePercent,
			want:    true,
		},
		{
			name:    "new feature one statement below threshold",
			summary: coverageSummary{CoveredStatements: 94_999, TotalStatements: 100_000},
			minimum: newFeatureMinimumCoveragePercent,
			want:    false,
		},
		{
			name:    "changed code exactly ninety percent",
			summary: coverageSummary{CoveredStatements: 9, TotalStatements: 10},
			minimum: changedCodeMinimumCoveragePercent,
			want:    true,
		},
		{
			name:    "changed code one statement below threshold",
			summary: coverageSummary{CoveredStatements: 89_999, TotalStatements: 100_000},
			minimum: changedCodeMinimumCoveragePercent,
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := coverageAtLeastPercent(test.summary, test.minimum); got != test.want {
				t.Fatalf(
					"coverageAtLeastPercent(%+v, %d) = %t, want %t",
					test.summary,
					test.minimum,
					got,
					test.want,
				)
			}
		})
	}
}

func TestCompareCoverageAllowsExactNewScopeFromEmptyBase(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath:    "docs/features/new.md",
		Ownerships: []featureOwnership{{Kind: "CODE", Pattern: "internal/new/**"}},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	base := emptyCoverageProfile()
	head := coverageProfile{
		Global: coverageSummary{CoveredStatements: 95, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"internal/new/feature.go": {CoveredStatements: 95, TotalStatements: 100},
		},
	}
	if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); len(failures) != 0 {
		t.Fatalf("exact empty-base threshold failures = %#v", failures)
	}
	head.Global.CoveredStatements = 94
	head.Files["internal/new/feature.go"] = coverageSummary{CoveredStatements: 94, TotalStatements: 100}
	if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); len(failures) != 2 {
		t.Fatalf("below empty-base threshold failures = %#v, want global and feature failures", failures)
	}
}

func TestInternalProductionAndCoverageBlockColumnBoundaries(t *testing.T) {
	file := "internal/example/provider.go"
	if !isCoverageRelevantGoFile(file) || !isProductionCodePath(file) {
		t.Fatal("internal production Go was excluded from coverage policy")
	}
	if !isCoverageRelevantGoFile("internal/example/provider_test.go") ||
		isProductionCodePath("internal/example/provider_test.go") {
		t.Fatal("internal Go test coverage relevance/production classification is invalid")
	}
	profile := coverageProfile{Blocks: map[string]map[string]coverageBlock{
		file: {
			"10.1,11.1": {
				File: file, Range: "10.1,11.1", StartLine: 10, StartCol: 1,
				EndLine: 11, EndCol: 1, Statements: 9, Covered: true,
			},
			"11.1,11.20": {
				File: file, Range: "11.1,11.20", StartLine: 11, StartCol: 1,
				EndLine: 11, EndCol: 20, Statements: 1,
			},
		},
	}}
	changed := map[string]map[int]bool{file: {11: true}}
	if got := changedCodeCoverage(changed, profile); got != (coverageSummary{0, 1}) {
		t.Fatalf("line-start block boundary coverage = %+v, want only uncovered 11.1 block", got)
	}
}

func TestExecutableCoverageScriptsAreRelevantProductionScope(t *testing.T) {
	for _, path := range []string{
		"scripts/coverage_delta.go",
		"scripts/feature_delta_guard.go",
		"scripts/featuretools_lib.go",
	} {
		if !isCoverageRelevantChange(path) || !isCoverageRelevantGoFile(path) ||
			!isProductionCodePath(path) {
			t.Fatalf("executable script %q was excluded from Go coverage scope", path)
		}
	}
	if !isCoverageRelevantGoFile("scripts/coverage_delta_test.go") ||
		isProductionCodePath("scripts/coverage_delta_test.go") {
		t.Fatal("script test coverage relevance/production classification is invalid")
	}
	for _, path := range []string{
		"scripts/testdata/fixture.go",
		"scripts/run-integration-tests.sh",
	} {
		if isCoverageRelevantGoFile(path) || isProductionCodePath(path) {
			t.Fatalf("non-production coverage script %q was included", path)
		}
	}

	const file = "scripts/coverage_delta.go"
	profile := coverageProfile{Blocks: map[string]map[string]coverageBlock{
		file: {
			"10.1,10.20": {
				File: file, Range: "10.1,10.20", StartLine: 10, StartCol: 1,
				EndLine: 10, EndCol: 20, Statements: 1, Covered: true,
			},
		},
	}}
	changed := map[string]map[int]bool{file: {10: true}}
	if got := changedCodeCoverage(changed, profile); got != (coverageSummary{1, 1}) {
		t.Fatalf("script changed-code coverage = %+v, want 1/1", got)
	}
	if !coveragePlanIncludesDirectory([]string{"pkg/example", "scripts"}, "scripts") ||
		coveragePlanIncludesDirectory([]string{"pkg/example"}, "scripts") {
		t.Fatal("script coverage plan directory detection is invalid")
	}
}

func TestParseCoverageBlockPreservesWindowsDrivePrefix(t *testing.T) {
	block, err := parseCoverageBlock(
		`C:\checkout`,
		"example.com/module",
		`C:\checkout\scripts\coverage_delta.go:12.3,14.5 7 1`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if block.StartLine != 12 || block.StartCol != 3 || block.EndLine != 14 ||
		block.EndCol != 5 || block.Statements != 7 || !block.Covered {
		t.Fatalf("Windows-drive coverage block = %#v", block)
	}
	file := strings.ReplaceAll(block.File, `\`, "/")
	if !strings.HasSuffix(file, "scripts/coverage_delta.go") {
		t.Fatalf("Windows-drive coverage file = %q", block.File)
	}
}

func TestCoverageParsingAndChangedStatusEdgeCases(t *testing.T) {
	for _, value := range []string{
		"1.1",
		"missing-column,2.2",
		"1.1,missing-column",
		"1,2.2",
		"0.1,2.2",
	} {
		if _, _, _, _, err := coverageRange(value); err == nil {
			t.Errorf("coverageRange(%q) unexpectedly succeeded", value)
		}
	}

	if got := changedCodeStatus(coverageSummary{}); got != "no changed executable Go statements" {
		t.Fatalf("empty changed-code status = %q", got)
	}
	if got := changedCodeStatus(coverageSummary{CoveredStatements: 9, TotalStatements: 10}); !strings.Contains(got, "90.00% (9/10)") {
		t.Fatalf("covered changed-code status = %q", got)
	}

	maxInt := int(^uint(0) >> 1)
	if !coverageRatioLess(
		coverageSummary{CoveredStatements: 0, TotalStatements: maxInt},
		coverageSummary{CoveredStatements: maxInt, TotalStatements: maxInt},
	) {
		t.Fatal("overflow-safe high-word ratio comparison accepted zero coverage")
	}
	if covered, total := exactCoverageRatio(coverageSummary{}); covered != 1 || total != 1 {
		t.Fatalf("empty exact coverage ratio = %d/%d", covered, total)
	}

	if _, err := changedGoLines(t.TempDir(), "missing-base", "missing-head"); err == nil {
		t.Fatal("changedGoLines accepted a non-repository")
	}
	lines, err := parseAddedDiffLines("@@ -1,2 +1,2 @@\n unchanged\n+added\n-removed\n")
	if err != nil || !lines[2] {
		t.Fatalf("context diff lines = %#v, %v", lines, err)
	}
}

func TestRunScriptCoverageCollectsRealProfilesAcrossRefFileDifferences(t *testing.T) {
	root := t.TempDir()
	writeScriptCoverageFixture(t, root, "go.mod", "module example.com/scriptcoverage\n\ngo 1.24\n")
	writeScriptCoverageFixture(t, root, "scripts/coverage_delta.go", `//go:build featuretools

package main

func main() {}

func coveredScriptValue() int { return sharedScriptValue() }
`)
	writeScriptCoverageFixture(t, root, "scripts/featuretools_lib.go", `//go:build featuretools

package main

func sharedScriptValue() int { return 42 }
`)

	baseProfile, err := runScriptCoverage(
		root,
		"base",
		"base-ref",
		"goolm,stdjson",
		filepath.Join(root, "base-profile"),
		os.Environ(),
	)
	if err != nil {
		t.Fatal(err)
	}
	baseTool := baseProfile.Files["scripts/coverage_delta.go"]
	baseShared := baseProfile.Files["scripts/featuretools_lib.go"]
	if baseTool.TotalStatements == 0 || baseShared.TotalStatements == 0 ||
		baseTool.CoveredStatements != 0 || baseShared.CoveredStatements != 0 {
		t.Fatalf("base explicit-file profile = tool %+v shared %+v", baseTool, baseShared)
	}

	writeScriptCoverageFixture(t, root, "scripts/coverage_delta_test.go", `//go:build featuretools

package main

import "testing"

func TestCoveredScriptValue(t *testing.T) {
	if coveredScriptValue() != 42 {
		t.Fatal("wrong value")
	}
}
`)
	headProfile, err := runScriptCoverage(
		root,
		"head",
		"head-ref",
		"featuretools,goolm",
		filepath.Join(root, "head-profile"),
		os.Environ(),
	)
	if err != nil {
		t.Fatal(err)
	}
	headTool := headProfile.Files["scripts/coverage_delta.go"]
	headShared := headProfile.Files["scripts/featuretools_lib.go"]
	if headTool.CoveredStatements == 0 || headShared.CoveredStatements == 0 ||
		len(headProfile.Blocks["scripts/coverage_delta.go"]) == 0 ||
		len(headProfile.Blocks["scripts/featuretools_lib.go"]) == 0 {
		t.Fatalf("head explicit-file profile = tool %+v shared %+v blocks %#v", headTool, headShared, headProfile.Blocks)
	}
	if headTool.CoveredStatements <= baseTool.CoveredStatements ||
		headShared.CoveredStatements <= baseShared.CoveredStatements {
		t.Fatalf("head tests did not add real block coverage: base=%+v/%+v head=%+v/%+v", baseTool, baseShared, headTool, headShared)
	}
}

func TestScriptCoverageGroupsAndFailuresAreFailClosed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if groups, err := scriptCoverageGroups(missing); err != nil || len(groups) != 0 {
		t.Fatalf("missing scripts groups = %#v, %v", groups, err)
	}
	if profile, err := runScriptCoverage(
		missing, "head", "head-ref", "", filepath.Join(missing, "profiles"), os.Environ(),
	); err != nil || profile.Global.TotalStatements != 0 {
		t.Fatalf("missing scripts profile = %#v, %v", profile, err)
	}

	notDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(notDirectory, "scripts"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runScriptCoverage(
		notDirectory,
		"head",
		"head-ref",
		"",
		filepath.Join(notDirectory, "profiles"),
		os.Environ(),
	); err == nil || !strings.Contains(err.Error(), "script coverage for head") {
		t.Fatalf("non-directory scripts error = %v", err)
	}

	missingModule := t.TempDir()
	writeScriptCoverageFixture(
		t,
		missingModule,
		"scripts/coverage_delta.go",
		"//go:build featuretools\n\npackage main\nfunc main() {}\n",
	)
	if _, err := runScriptCoverage(
		missingModule,
		"head",
		"head-ref",
		"",
		filepath.Join(missingModule, "profiles"),
		os.Environ(),
	); err == nil || !strings.Contains(err.Error(), "read go.mod") {
		t.Fatalf("missing script coverage module error = %v", err)
	}

	root := t.TempDir()
	writeScriptCoverageFixture(t, root, "go.mod", "module example.com/scriptcoveragefailure\n\ngo 1.24\n")
	writeScriptCoverageFixture(t, root, "scripts/featuretools_lib.go", "//go:build featuretools\n\npackage main\n")
	groups, err := scriptCoverageGroups(root)
	if err != nil || !reflect.DeepEqual(groups, []scriptCoverageGroup{{
		Name: "featuretools_lib", Files: []string{"scripts/featuretools_lib.go"},
	}}) {
		t.Fatalf("shared-only groups = %#v, %v", groups, err)
	}
	unsafeRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unsafeRoot, "scripts", "featuretools_lib.go"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := scriptCoverageGroups(unsafeRoot); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("unsafe shared coverage file error = %v", err)
	}
	if _, err := scriptCoverageFileAvailable(
		root,
		"scripts/"+strings.Repeat("x", 5000)+".go",
	); err == nil {
		t.Fatal("overlong script coverage path was accepted")
	}

	writeScriptCoverageFixture(t, root, "scripts/coverage_delta.go", "not valid Go")
	blockedProfile := filepath.Join(root, "profile-file")
	if err := os.WriteFile(blockedProfile, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runScriptCoverage(
		root, "head", "head-ref", "", blockedProfile, os.Environ(),
	); err == nil || !strings.Contains(err.Error(), "create script coverage directory") {
		t.Fatalf("blocked profile directory error = %v", err)
	}
	if _, err := runScriptCoverage(
		root, "base", "base-ref", "", filepath.Join(root, "profiles"), os.Environ(),
	); err == nil || !strings.Contains(err.Error(), "script coverage for base group coverage_delta") {
		t.Fatalf("invalid explicit-file source error = %v", err)
	}

	if got := scriptCoverageBuildTags(""); got != "featuretools" {
		t.Fatalf("empty script coverage tags = %q", got)
	}
	if got := scriptCoverageBuildTags(" goolm,stdjson, "); got != "featuretools,goolm,stdjson" {
		t.Fatalf("merged script coverage tags = %q", got)
	}
	if got := scriptCoverageBuildTags("goolm featuretools"); got != "goolm featuretools" {
		t.Fatalf("existing script coverage tags = %q", got)
	}
	if got := scriptCoverageTestFiles("future_tool.go"); !reflect.DeepEqual(
		got,
		[]string{"scripts/future_tool_test.go"},
	) {
		t.Fatalf("future tool test mapping = %#v", got)
	}
	if got := scriptCoverageTestFiles("feature_delta_guard.go"); !reflect.DeepEqual(
		got,
		[]string{"scripts/featuretools_lib_test.go"},
	) {
		t.Fatalf("feature delta test mapping = %#v", got)
	}

	unsafeTestRoot := t.TempDir()
	writeScriptCoverageFixture(t, unsafeTestRoot, "scripts/featuretools_lib.go", "package main\n")
	writeScriptCoverageFixture(t, unsafeTestRoot, "scripts/coverage_delta.go", "package main\n")
	if err := os.MkdirAll(
		filepath.Join(unsafeTestRoot, "scripts", "coverage_delta_test.go"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := scriptCoverageGroups(unsafeTestRoot); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("unsafe script test coverage file error = %v", err)
	}
}

func writeScriptCoverageFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompareCoverageRequiresNinetyFivePercentForNewFeature(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath: "docs/features/example.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/example/**"},
		},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	base := coverageProfile{
		Global: coverageSummary{CoveredStatements: 900, TotalStatements: 1000},
		Files: map[string]coverageSummary{
			"pkg/existing/existing.go": {CoveredStatements: 900, TotalStatements: 1000},
		},
	}
	head := func(featureCovered int) coverageProfile {
		return coverageProfile{
			Global: coverageSummary{CoveredStatements: 901 + featureCovered, TotalStatements: 1100},
			Files: map[string]coverageSummary{
				"pkg/existing/existing.go": {CoveredStatements: 901, TotalStatements: 1000},
				"pkg/example/example.go":   {CoveredStatements: featureCovered, TotalStatements: 100},
			},
		}
	}

	if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head(95)); len(failures) != 0 {
		t.Fatalf("exact threshold failures = %#v", failures)
	}
	want := []string{
		"docs/features/example.md new Go feature coverage is below 95%: 94.00% (94/100)",
	}
	if got := compareCoverage([]featureSpecMetadata{spec}, plan, base, head(94)); !reflect.DeepEqual(got, want) {
		t.Fatalf("below threshold failures = %#v, want %#v", got, want)
	}
}

func TestCompareCoverageRequiresNinetyPercentForChangedBlocks(t *testing.T) {
	plan := coveragePlan{ChangedLines: map[string]map[int]bool{
		"pkg/example/example.go": {10: true, 11: true, 20: true},
	}}
	base := coverageProfile{Global: coverageSummary{CoveredStatements: 100, TotalStatements: 100}}
	head := func(coveredStatements, uncoveredStatements int) coverageProfile {
		return coverageProfile{
			Global: coverageSummary{CoveredStatements: 100, TotalStatements: 100},
			Blocks: map[string]map[string]coverageBlock{
				"pkg/example/example.go": {
					"10.1,12.1": {
						File: "pkg/example/example.go", Range: "10.1,12.1",
						StartLine: 10, EndLine: 12, Statements: coveredStatements, Covered: true,
					},
					"20.1,20.5": {
						File: "pkg/example/example.go", Range: "20.1,20.5",
						StartLine: 20, EndLine: 20, Statements: uncoveredStatements,
					},
				},
			},
		}
	}

	exact := head(9, 1)
	if summary := changedCodeCoverage(plan.ChangedLines, exact); summary != (coverageSummary{9, 10}) {
		t.Fatalf("exact changed coverage = %+v, want 9/10", summary)
	}
	if failures := compareCoverage(nil, plan, base, exact); len(failures) != 0 {
		t.Fatalf("exact threshold failures = %#v", failures)
	}
	below := head(8, 2)
	want := []string{"changed production Go coverage is below 90%: 80.00% (8/10)"}
	if got := compareCoverage(nil, plan, base, below); !reflect.DeepEqual(got, want) {
		t.Fatalf("below threshold failures = %#v, want %#v", got, want)
	}
}

func TestChangedCoverageDeduplicatesSpanningBlocksAndFeatureOwnershipCanOverlap(t *testing.T) {
	file := "pkg/example/example.go"
	profile := coverageProfile{
		Files: map[string]coverageSummary{
			file: {CoveredStatements: 9, TotalStatements: 10},
		},
		Blocks: map[string]map[string]coverageBlock{
			file: {
				"10.1,12.1": {
					File: file, Range: "10.1,12.1", StartLine: 10, EndLine: 12,
					Statements: 9, Covered: true,
				},
				"20.1,20.5": {
					File: file, Range: "20.1,20.5", StartLine: 20, EndLine: 20,
					Statements: 1,
				},
			},
		},
	}
	changedLines := map[string]map[int]bool{
		file: {10: true, 11: true, 12: true, 20: true},
	}
	if got := changedCodeCoverage(changedLines, profile); got != (coverageSummary{9, 10}) {
		t.Fatalf("changed coverage = %+v, want each block counted once as 9/10", got)
	}

	specs := []featureSpecMetadata{
		{RelPath: "docs/features/first.md", Ownerships: []featureOwnership{{Kind: "CODE", Pattern: "pkg/example/**"}}},
		{
			RelPath:    "docs/features/second.md",
			Ownerships: []featureOwnership{{Kind: "CODE", Pattern: "pkg/example/example.go"}},
		},
	}
	got := featureCoverage(specs, profile)
	for _, spec := range specs {
		if got[spec.RelPath] != (coverageSummary{9, 10}) {
			t.Fatalf("%s coverage = %+v, want 9/10", spec.RelPath, got[spec.RelPath])
		}
	}
}

func TestCompareCoverageUsesHybridPolicyForExistingGlobalAndFeature(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath: "docs/features/example.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/example/**"},
		},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	base := coverageProfile{
		Global: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"pkg/example/example.go": {CoveredStatements: 80, TotalStatements: 100},
		},
	}
	head := coverageProfile{
		Global: coverageSummary{CoveredStatements: 69, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"pkg/example/example.go": {CoveredStatements: 69, TotalStatements: 100},
		},
	}

	want := []string{
		"scoped Go coverage regressed: uncovered statement debt 20 -> 31 and coverage 80.00% (80/100) -> 69.00% (69/100)",
		"docs/features/example.md Go coverage regressed: uncovered statement debt 20 -> 31 and coverage 80.00% (80/100) -> 69.00% (69/100)",
	}
	if got := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); !reflect.DeepEqual(got, want) {
		t.Fatalf("compareCoverage() = %#v, want %#v", got, want)
	}
}

func TestCompareCoverageAllowsDebtIncreaseAtStableOrImprovedPercentage(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath: "docs/features/example.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/example/**"},
		},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	base := coverageProfile{
		Global: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"pkg/example/example.go": {CoveredStatements: 80, TotalStatements: 100},
		},
	}
	for _, summary := range []coverageSummary{
		{CoveredStatements: 160, TotalStatements: 200},
		{CoveredStatements: 162, TotalStatements: 200},
	} {
		head := coverageProfile{
			Global: summary,
			Files:  map[string]coverageSummary{"pkg/example/example.go": summary},
		}
		if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); len(failures) != 0 {
			t.Fatalf("head %+v failures = %#v", summary, failures)
		}
	}
}

func TestCompareCoverageAllowsCoveredCodeDeletionWithUnchangedDebt(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath: "docs/features/example.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/example/**"},
		},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	baseSummary := coverageSummary{CoveredStatements: 80, TotalStatements: 100}
	headSummary := coverageSummary{CoveredStatements: 70, TotalStatements: 90}
	base := coverageProfile{
		Global: baseSummary,
		Files:  map[string]coverageSummary{"pkg/example/example.go": baseSummary},
	}
	head := coverageProfile{
		Global: headSummary,
		Files:  map[string]coverageSummary{"pkg/example/example.go": headSummary},
	}
	if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); len(failures) != 0 {
		t.Fatalf("covered deletion failures = %#v", failures)
	}
}

func TestCoverageEnvironmentIsolatesRefState(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HOME=/shared/user-home",
		"PICOCLAW_HOME=/shared/home",
		"picoclaw_home=/duplicate/home",
		"GOCACHE=/shared/build-cache",
		"GOMODCACHE=/shared/module-cache",
		"GOTOOLCHAIN=local",
		"AWS_ACCESS_KEY_ID=operator-access-key",
		"AWS_SECRET_ACCESS_KEY=operator-secret-key",
		"AWS_PROFILE=operator-profile",
		"AWS_SHARED_CREDENTIALS_FILE=/operator/aws-credentials",
		"OPENAI_API_KEY=operator-openai-key",
		"ANTHROPIC_API_KEY=operator-anthropic-key",
		"GITHUB_TOKEN=operator-github-token",
		"GITHUB_PAT=operator-github-pat",
		"GH_TOKEN=operator-gh-token",
		"GH_PAT=operator-gh-pat",
		"SSH_AUTH_SOCK=/operator/ssh-agent.sock",
		"GOOGLE_APPLICATION_CREDENTIALS=/operator/google.json",
		"AZURE_CLIENT_SECRET=operator-azure-secret",
		"KUBECONFIG=/operator/kubeconfig",
		"DOCKER_HOST=unix:///operator/docker.sock",
		"GIT_ASKPASS=/operator/askpass",
		"GIT_TERMINAL_PROMPT=1",
		"GCM_INTERACTIVE=always",
		"NETRC=/operator/netrc",
		"SERVICE_API_KEY=operator-generic-key",
		"NOTIFY_SOCKET=/operator/notify.sock",
		"LISTEN_FDS=3",
		"GOAUTH=/operator/goauth-helper",
		"GOENV=/operator/goenv",
		"VALUE=with=equals",
	}
	original := append([]string(nil), base...)

	caches := goCachePaths{Build: "/cache/build", Modules: "/cache/modules"}
	baseEnvironment := coverageEnvironment(base, "/isolated/base", caches)
	headEnvironment := coverageEnvironment(base, "/isolated/head", caches)

	if !reflect.DeepEqual(base, original) {
		t.Fatalf("coverageEnvironment() mutated its input: got %#v, want %#v", base, original)
	}
	assertEnvironmentValue(t, baseEnvironment, "HOME", "/isolated/base")
	assertEnvironmentValue(t, headEnvironment, "HOME", "/isolated/head")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_HOME", "/isolated/base/.picoclaw")
	assertEnvironmentValue(t, headEnvironment, "PICOCLAW_HOME", "/isolated/head/.picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_CONFIG", "/isolated/base/.picoclaw/config.json")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_BINARY", "/isolated/base/bin/picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "XDG_RUNTIME_DIR", "/isolated/base/.xdg/runtime")
	assertEnvironmentValue(t, baseEnvironment, "TMPDIR", "/isolated/base/.tmp")
	assertEnvironmentValue(t, baseEnvironment, "GNUPGHOME", "/isolated/base/.gnupg")
	assertEnvironmentValue(t, baseEnvironment, "GIT_CONFIG_NOSYSTEM", "1")
	assertEnvironmentValue(
		t,
		baseEnvironment,
		"DBUS_SESSION_BUS_ADDRESS",
		"unix:path=/isolated/base/.no-systemd-bus",
	)
	assertEnvironmentValue(t, baseEnvironment, "GOCACHE", "/cache/build")
	assertEnvironmentValue(t, baseEnvironment, "GOMODCACHE", "/cache/modules")
	assertEnvironmentValue(t, baseEnvironment, "GOTOOLCHAIN", "auto")
	assertEnvironmentValue(t, baseEnvironment, "AWS_EC2_METADATA_DISABLED", "true")
	assertEnvironmentValue(t, baseEnvironment, "GIT_TERMINAL_PROMPT", "0")
	assertEnvironmentValue(t, baseEnvironment, "GCM_INTERACTIVE", "never")
	assertEnvironmentValue(t, baseEnvironment, "GOAUTH", "off")
	assertEnvironmentValue(t, baseEnvironment, "GOENV", "off")
	assertEnvironmentValue(t, baseEnvironment, "PATH", "/bin")
	assertEnvironmentValue(t, baseEnvironment, "VALUE", "with=equals")
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_PROFILE",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SHARED_CREDENTIALS_FILE",
		"AZURE_CLIENT_SECRET",
		"DOCKER_HOST",
		"GH_TOKEN",
		"GH_PAT",
		"GITHUB_TOKEN",
		"GITHUB_PAT",
		"GIT_ASKPASS",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"KUBECONFIG",
		"NETRC",
		"NOTIFY_SOCKET",
		"LISTEN_FDS",
		"OPENAI_API_KEY",
		"SERVICE_API_KEY",
		"SSH_AUTH_SOCK",
	} {
		assertEnvironmentMissing(t, baseEnvironment, name)
	}
	fallbackEnvironment := coverageFallbackHomeEnvironment(baseEnvironment)
	assertEnvironmentValue(t, fallbackEnvironment, "HOME", "/isolated/base")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_HOME", "")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_CONFIG", "")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_BINARY", "/isolated/base/bin/picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_HOME", "/isolated/base/.picoclaw")
}

func TestPrepareCoverageStorageDefersConfigUntilAfterUnitCoverage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "coverage-home")
	if err := prepareCoverageStorage(home); err != nil {
		t.Fatalf("prepareCoverageStorage() error = %v", err)
	}
	configPath := filepath.Join(home, ".picoclaw", "config.json")
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coverage config existed during fallback-home unit tests: %v", err)
	}
	if err := writeCoverageConfig(home); err != nil {
		t.Fatalf("writeCoverageConfig() error = %v", err)
	}
	if info, err := os.Stat(configPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("coverage config = (%v, %v), want regular file", info, err)
	}
}

func TestPrepareCoverageHomeCreatesIsolatedRuntimeState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "coverage-home")
	if err := prepareCoverageHome(home); err != nil {
		t.Fatalf("prepareCoverageHome() error = %v", err)
	}
	configPath := filepath.Join(home, ".picoclaw", "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read coverage config: %v", err)
	}
	var configFile struct {
		Agents struct {
			Defaults struct {
				Workspace string `json:"workspace"`
			} `json:"defaults"`
		} `json:"agents"`
		Gateway struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"gateway"`
		Events struct {
			Ingress struct {
				DatabasePath string `json:"database_path"`
			} `json:"ingress"`
		} `json:"events"`
	}
	if err = json.Unmarshal(raw, &configFile); err != nil {
		t.Fatalf("decode coverage config: %v", err)
	}
	wantWorkspace := filepath.Join(home, ".picoclaw", "workspace")
	wantDB := filepath.Join(wantWorkspace, "eventing", "events.db")
	if configFile.Agents.Defaults.Workspace != wantWorkspace ||
		configFile.Events.Ingress.DatabasePath != wantDB ||
		configFile.Gateway.Host != "127.0.0.1" || configFile.Gateway.Port <= 0 {
		t.Fatalf("coverage config escaped runtime: %#v", configFile)
	}
	if info, statErr := os.Stat(wantDB); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("coverage event database = (%v, %v), want regular file", info, statErr)
	}
}

func TestRunCoverageCommandRetriesAnyBaseFailureOnce(t *testing.T) {
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			if attempts == 1 {
				return []byte("unclassified baseline failure"), wantErr
			}
			return []byte("ok"), nil
		},
	)
	if err != nil || string(out) != "ok" || !retried || attempts != 2 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandDoesNotRetryHeadFailure(t *testing.T) {
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"head",
		func() ([]byte, error) {
			attempts++
			return []byte("head failure"), wantErr
		},
	)
	if !errors.Is(err, wantErr) || string(out) != "head failure" || retried || attempts != 1 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandDoesNotRetrySuccessfulBase(t *testing.T) {
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			return []byte("ok"), nil
		},
	)
	if err != nil || string(out) != "ok" || retried || attempts != 1 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandFailsDeterministicBaseFailureAfterOneRetry(t *testing.T) {
	firstErr := errors.New("first exit status 1")
	secondErr := errors.New("second exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			if attempts == 1 {
				return []byte("first baseline failure"), firstErr
			}
			return []byte("second baseline failure"), secondErr
		},
	)
	if !errors.Is(err, secondErr) || string(out) != "second baseline failure" ||
		!retried || attempts != 2 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestTrimCommandOutputPreservesFailureContextAndTail(t *testing.T) {
	failure := "--- FAIL: TestLeaseLoss (0.01s)\n" +
		"    worker_test.go:42: lease expired before admission\n" +
		"FAIL\nFAIL\tgithub.com/sipeed/picoclaw/pkg/reviews\t0.01s\n"
	largeContextLine := strings.Repeat("successful package with verbose coverage ", 60) + "\n"
	output := strings.Repeat("earlier output\n", 500) + strings.Repeat(largeContextLine, 6) + failure +
		strings.Repeat("later successful package with verbose coverage\n", 500) + "final output line"

	trimmed := trimCommandOutput([]byte(output))
	if len(trimmed) > 12000 {
		t.Fatalf("trimmed output length = %d, want at most 12000", len(trimmed))
	}
	for _, want := range []string{
		"failure markers:",
		"--- FAIL: TestLeaseLoss",
		"worker_test.go:42: lease expired before admission",
		"FAIL\tgithub.com/sipeed/picoclaw/pkg/reviews",
		"command output truncated; failure context preserved above",
		"final output line",
	} {
		if !strings.Contains(trimmed, want) {
			t.Fatalf("trimmed output does not contain %q:\n%s", want, trimmed)
		}
	}
	if !utf8.ValidString(trimmed) {
		t.Fatal("trimmed output is not valid UTF-8")
	}
}

func TestTrimCommandOutputPreservesBuildAndPanicFailures(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name: "build failure",
			output: "# github.com/sipeed/picoclaw/pkg/example\n" +
				"pkg/example/example.go:42:9: undefined: missingSymbol\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example [build failed]\n",
			want: []string{"undefined: missingSymbol", "FAIL\tgithub.com/sipeed/picoclaw/pkg/example [build failed]"},
		},
		{
			name: "test timeout",
			output: "panic: test timed out after 10m0s\n" +
				"running tests:\n\tTestBlocked (10m0s)\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example\t600.00s\n",
			want: []string{
				"panic: test timed out after 10m0s",
				"TestBlocked",
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			largeLine := strings.Repeat("coverage context π ", 150) + "\n"
			output := strings.Repeat("earlier output\n", 500) +
				strings.Repeat(largeLine, 6) + test.output +
				strings.Repeat("later output\n", 1000) + "final output line"
			trimmed := trimCommandOutput([]byte(output))
			if len(trimmed) > 12000 {
				t.Fatalf("trimmed output length = %d, want at most 12000", len(trimmed))
			}
			for _, want := range append(test.want, "final output line") {
				if !strings.Contains(trimmed, want) {
					t.Fatalf("trimmed output does not contain %q:\n%s", want, trimmed)
				}
			}
			if !utf8.ValidString(trimmed) {
				t.Fatal("trimmed output is not valid UTF-8")
			}
		})
	}
}

func TestCommandOutputClippingPreservesUTF8Boundaries(t *testing.T) {
	line := strings.Repeat("a", 252) + "π" + strings.Repeat("b", 600)
	clipped := clipCommandFailureLine(line)
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipped failure line is not valid UTF-8: %q", clipped)
	}
	if tail := commandOutputTail("aπbc", 3); tail != "bc" || !utf8.ValidString(tail) {
		t.Fatalf("commandOutputTail() = %q, want valid UTF-8 %q", tail, "bc")
	}
}

func assertEnvironmentValue(t *testing.T, environment []string, name, want string) {
	t.Helper()
	var values []string
	for _, entry := range environment {
		entryName, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(entryName, name) {
			values = append(values, value)
		}
	}
	if len(values) != 1 || values[0] != want {
		t.Fatalf("environment %s values = %#v, want [%q]", name, values, want)
	}
}

func assertEnvironmentMissing(t *testing.T, environment []string, name string) {
	t.Helper()
	for _, entry := range environment {
		entryName, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(entryName, name) {
			t.Fatalf("environment unexpectedly contains %s", name)
		}
	}
}
