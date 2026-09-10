//go:build featuretools

package main

import (
	"bytes"
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
	"time"
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

const relocationFixtureModule = "example.com/relocation"

type internalRelocationFixture struct {
	Root string
	Base string
}

func newInternalRelocationFixture(t *testing.T) internalRelocationFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	root := t.TempDir()
	runCoverageDeltaTestGit(t, root, "init", "--quiet")
	runCoverageDeltaTestGit(t, root, "config", "user.email", "coverage-relocation@example.invalid")
	runCoverageDeltaTestGit(t, root, "config", "user.name", "Coverage Relocation Test")
	runCoverageDeltaTestGit(t, root, "config", "commit.gpgSign", "false")
	runCoverageDeltaTestGit(t, root, "config", "core.fileMode", "true")
	hooks := filepath.Join(root, "disabled-hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	runCoverageDeltaTestGit(t, root, "config", "core.hooksPath", hooks)

	files := map[string]string{
		"go.mod": "module " + relocationFixtureModule + "\n\ngo 1.25\n",
		"pkg/alpha/alpha.go": `package alpha

func Value() int { return 1 }
`,
		"pkg/alpha/alpha_test.go": `package alpha

import "testing"

func TestValue(t *testing.T) { t.Log(Value()) }
`,
		"pkg/second/second.go": `package second

func Value() int { return 4 }
`,
		"pkg/store/store.go": `package store

func Value() int { return 2 }
`,
		"pkg/store/store_linux.go": `//go:build linux

package store

func PlatformValue() int { return 3 }
`,
		"pkg/store/store_test.go": `package store

import "testing"

func TestValue(t *testing.T) {
	if Value() != 2 {
		t.Fatal("wrong value")
	}
}
`,
		"pkg/store/testdata/schema.sql": "CREATE TABLE fixture (id INTEGER);\n",
		"pkg/consumer/consumer.go": relocationFixtureConsumerSource(
			relocationFixtureModule+"/pkg/store",
			relocationFixtureModule+"/pkg/alpha",
			"return alpha.Value() + store.Value()",
			"",
		),
		"pkg/consumer/consumer_test.go": "package consumer_test\n\nimport _ \"" +
			relocationFixtureModule + "/pkg/store\"\n",
		"docs/features/fixture.md": "# Fixture\n",
	}
	for path, contents := range files {
		writeCoverageDeltaTestFile(t, root, path, contents)
	}
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "base")
	return internalRelocationFixture{
		Root: root,
		Base: strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD")),
	}
}

func relocationFixtureConsumerSource(storeImport, alphaImport, body, buildTag string) string {
	prefix := ""
	if buildTag != "" {
		prefix = "//go:build " + buildTag + "\n\n"
	}
	firstImport, secondImport := alphaImport, storeImport
	if secondImport < firstImport {
		firstImport, secondImport = secondImport, firstImport
	}
	return prefix + `package consumer

import (
	"` + firstImport + `"
	"` + secondImport + `"
)

func Value() int {
	` + body + `
}
`
}

func (fixture internalRelocationFixture) moveFile(t *testing.T, relative string) {
	t.Helper()
	source := filepath.Join(fixture.Root, "pkg", "store", filepath.FromSlash(relative))
	destination := filepath.Join(fixture.Root, "internal", "store", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
}

func (fixture internalRelocationFixture) moveCompletePackage(t *testing.T) {
	t.Helper()
	for _, path := range []string{
		"store.go",
		"store_linux.go",
		"store_test.go",
		"testdata/schema.sql",
	} {
		fixture.moveFile(t, path)
	}
}

func (fixture internalRelocationFixture) rewriteConsumers(t *testing.T) {
	t.Helper()
	writeCoverageDeltaTestFile(
		t,
		fixture.Root,
		"pkg/consumer/consumer.go",
		relocationFixtureConsumerSource(
			relocationFixtureModule+"/internal/store",
			relocationFixtureModule+"/pkg/alpha",
			"return alpha.Value() + store.Value()",
			"",
		),
	)
	writeCoverageDeltaTestFile(
		t,
		fixture.Root,
		"pkg/consumer/consumer_test.go",
		"package consumer_test\n\nimport _ \""+relocationFixtureModule+"/internal/store\"\n",
	)
}

func (fixture internalRelocationFixture) commitHead(t *testing.T) string {
	t.Helper()
	runCoverageDeltaTestGit(t, fixture.Root, "add", "--all")
	runCoverageDeltaTestGit(t, fixture.Root, "commit", "--quiet", "-m", "head")
	return strings.TrimSpace(runCoverageDeltaTestGit(t, fixture.Root, "rev-parse", "HEAD"))
}

func TestVerifiedInternalPackageRelocationLimitsFeatureImpactButKeepsPackageScope(t *testing.T) {
	t.Parallel()
	fixture := newInternalRelocationFixture(t)
	fixture.moveCompletePackage(t)
	fixture.rewriteConsumers(t)
	writeCoverageDeltaTestFile(t, fixture.Root, "docs/features/fixture.md", "# Fixture\n\nUpdated.\n")
	head := fixture.commitHead(t)

	relocation, err := verifiedInternalPackageRelocationChanges(
		fixture.Root,
		fixture.Base,
		head,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantImportOnly := map[string]bool{
		"pkg/consumer/consumer.go":      true,
		"pkg/consumer/consumer_test.go": true,
	}
	if !reflect.DeepEqual(relocation.ImportOnlyFiles, wantImportOnly) {
		t.Fatalf(
			"verified relocation import-only files = %#v, want %#v",
			relocation.ImportOnlyFiles,
			wantImportOnly,
		)
	}

	movedSpec := featureSpecMetadata{
		RelPath: "docs/features/moved.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/store/**", SpecRelPath: "docs/features/moved.md"},
			{Kind: "CODE", Pattern: "internal/store/**", SpecRelPath: "docs/features/moved.md"},
		},
	}
	consumerSpec := featureSpecMetadata{
		RelPath: "docs/features/consumer.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/consumer/**", SpecRelPath: "docs/features/consumer.md"},
		},
	}
	plan, err := buildCoveragePlan(
		fixture.Root,
		fixture.Base,
		head,
		[]featureSpecMetadata{movedSpec, consumerSpec},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ImpactedFeature[movedSpec.RelPath] {
		t.Fatal("direct moved package owner was not impacted")
	}
	if plan.ImpactedFeature[consumerSpec.RelPath] {
		t.Fatal("import-only consumer owner was impacted")
	}
	wantRelocatedFiles := map[string]string{
		"pkg/store/store.go":            "internal/store/store.go",
		"pkg/store/store_linux.go":      "internal/store/store_linux.go",
		"pkg/store/store_test.go":       "internal/store/store_test.go",
		"pkg/store/testdata/schema.sql": "internal/store/testdata/schema.sql",
	}
	if !reflect.DeepEqual(plan.RelocatedFiles, wantRelocatedFiles) {
		t.Fatalf("relocated files = %#v, want %#v", plan.RelocatedFiles, wantRelocatedFiles)
	}
	for _, dir := range []string{"internal/store", "pkg/consumer"} {
		if !slices.Contains(plan.CoverPackageDirs, dir) {
			t.Errorf("cover package dirs %v omit changed package %q", plan.CoverPackageDirs, dir)
		}
		if !slices.Contains(plan.TestPackageDirs, dir) {
			t.Errorf("test package dirs %v omit changed package %q", plan.TestPackageDirs, dir)
		}
	}
	if !plan.GlobalRelevant {
		t.Fatal("verified relocation was not globally coverage-relevant")
	}
}

func TestVerifiedInternalPackageRelocationFailsClosed(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, internalRelocationFixture){
		"content change below R100": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "internal/store/store.go", `package store

func Value() int { return 99 }
`)
		},
		"copy": func(t *testing.T, fixture internalRelocationFixture) {
			for _, relative := range []string{"store.go", "store_linux.go", "store_test.go"} {
				source := filepath.Join(fixture.Root, "pkg", "store", relative)
				contents, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				writeCoverageDeltaTestFile(t, fixture.Root, "internal/store/"+relative, string(contents))
				writeCoverageDeltaTestFile(t, fixture.Root, "pkg/store/"+relative, string(contents)+"\n// retained source\n")
			}
			fixture.rewriteConsumers(t)
		},
		"mixed consumer import and code": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(
				t,
				fixture.Root,
				"pkg/consumer/consumer.go",
				relocationFixtureConsumerSource(
					relocationFixtureModule+"/internal/store",
					relocationFixtureModule+"/pkg/alpha",
					"return alpha.Value() + store.Value() + 1",
					"",
				),
			)
		},
		"unbacked module import swap": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(
				t,
				fixture.Root,
				"pkg/consumer/consumer.go",
				relocationFixtureConsumerSource(
					relocationFixtureModule+"/internal/store",
					relocationFixtureModule+"/internal/alpha",
					"return alpha.Value() + store.Value()",
					"",
				),
			)
		},
		"consumer build tag edit": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(
				t,
				fixture.Root,
				"pkg/consumer/consumer.go",
				relocationFixtureConsumerSource(
					relocationFixtureModule+"/internal/store",
					relocationFixtureModule+"/pkg/alpha",
					"return alpha.Value() + store.Value()",
					"linux",
				),
			)
		},
		"consumer declaration edit": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			path := filepath.Join(fixture.Root, "pkg", "consumer", "consumer.go")
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeCoverageDeltaTestFile(
				t,
				fixture.Root,
				"pkg/consumer/consumer.go",
				strings.Replace(string(contents), "func Value() int", "func ChangedValue() int", 1),
			)
		},
		"partial package move": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveFile(t, "store.go")
			fixture.rewriteConsumers(t)
		},
		"go.mod change": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(
				t,
				fixture.Root,
				"go.mod",
				"module "+relocationFixtureModule+"\n\ngo 1.24\n",
			)
		},
		"go.sum change": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "go.sum", "example invalid\n")
		},
		"file mode change": func(t *testing.T, fixture internalRelocationFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("Win32 does not reliably expose executable-bit changes")
			}
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			if err := os.Chmod(filepath.Join(fixture.Root, "internal", "store", "store.go"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"second relocation": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			source := filepath.Join(fixture.Root, "pkg", "second", "second.go")
			destination := filepath.Join(fixture.Root, "internal", "second", "second.go")
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(source, destination); err != nil {
				t.Fatal(err)
			}
		},
		"unrelated production Go edit": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "pkg/alpha/alpha.go", `package alpha

func Value() int { return 9 }
`)
		},
		"unrelated test Go edit": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "pkg/alpha/alpha_test.go", `package alpha

import "testing"

func TestValue(t *testing.T) { t.Log("changed", Value()) }
`)
		},
		"unrelated added Go file": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "pkg/extra/extra.go", `package extra

func Value() int { return 1 }
`)
		},
		"unmoved package asset": func(t *testing.T, fixture internalRelocationFixture) {
			for _, path := range []string{"store.go", "store_linux.go", "store_test.go"} {
				fixture.moveFile(t, path)
			}
			fixture.rewriteConsumers(t)
		},
		"unrelated non-doc file": func(t *testing.T, fixture internalRelocationFixture) {
			fixture.moveCompletePackage(t)
			fixture.rewriteConsumers(t)
			writeCoverageDeltaTestFile(t, fixture.Root, "README.md", "unrelated\n")
		},
	}

	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newInternalRelocationFixture(t)
			mutate(t, fixture)
			head := fixture.commitHead(t)
			got, err := verifiedInternalPackageRelocationChanges(
				fixture.Root,
				fixture.Base,
				head,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.ImportOnlyFiles) != 0 || len(got.RelocatedFiles) != 0 {
				t.Fatalf("fail-closed relocation files = %#v, want none", got)
			}
			consumerSpec := featureSpecMetadata{
				RelPath: "docs/features/consumer.md",
				Ownerships: []featureOwnership{
					{Kind: "CODE", Pattern: "pkg/consumer/**", SpecRelPath: "docs/features/consumer.md"},
				},
			}
			plan, err := buildCoveragePlan(
				fixture.Root,
				fixture.Base,
				head,
				[]featureSpecMetadata{consumerSpec},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.ImpactedFeature[consumerSpec.RelPath] {
				t.Fatal("fail-closed comparison did not restore consumer feature impact")
			}
		})
	}
}

func TestVerifiedInternalPackageRelocationRequiresModuleIdentity(t *testing.T) {
	t.Parallel()
	fixture := newInternalRelocationFixture(t)
	writeCoverageDeltaTestFile(t, fixture.Root, "go.mod", "go 1.25\n")
	runCoverageDeltaTestGit(t, fixture.Root, "add", "go.mod")
	runCoverageDeltaTestGit(t, fixture.Root, "commit", "--quiet", "--amend", "--no-edit")
	fixture.Base = strings.TrimSpace(runCoverageDeltaTestGit(t, fixture.Root, "rev-parse", "HEAD"))
	fixture.moveCompletePackage(t)
	fixture.rewriteConsumers(t)
	head := fixture.commitHead(t)

	relocation, err := verifiedInternalPackageRelocationChanges(fixture.Root, fixture.Base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(relocation.ImportOnlyFiles) != 0 || len(relocation.RelocatedFiles) != 0 {
		t.Fatalf("module-less relocation classified as verified: %#v", relocation)
	}

	missing := newInternalRelocationFixture(t)
	if err := os.Remove(filepath.Join(missing.Root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	runCoverageDeltaTestGit(t, missing.Root, "add", "--all")
	runCoverageDeltaTestGit(t, missing.Root, "commit", "--quiet", "--amend", "--no-edit")
	missing.Base = strings.TrimSpace(runCoverageDeltaTestGit(t, missing.Root, "rev-parse", "HEAD"))
	missing.moveCompletePackage(t)
	missing.rewriteConsumers(t)
	missingHead := missing.commitHead(t)
	if _, err := verifiedInternalPackageRelocationChanges(
		missing.Root,
		missing.Base,
		missingHead,
	); err == nil {
		t.Fatal("relocation classifier accepted a repository without go.mod")
	}
}

func TestGofmtImportOnlyRelocationChangeRejectsPiggybackEdits(t *testing.T) {
	t.Parallel()
	oldImport := relocationFixtureModule + "/pkg/store"
	newImport := relocationFixtureModule + "/internal/store"
	alphaImport := relocationFixtureModule + "/pkg/alpha"
	externalOld := "example.net/dependency/old"
	externalNew := "example.net/dependency/new"
	replacements := map[string]string{oldImport: newImport}
	base := relocationFixtureConsumerSource(
		oldImport,
		alphaImport,
		"return alpha.Value() + store.Value()",
		"",
	)

	tests := map[string]struct {
		head string
		want bool
	}{
		"gofmt import reorder": {
			head: relocationFixtureConsumerSource(
				newImport,
				alphaImport,
				"return alpha.Value() + store.Value()",
				"",
			),
			want: true,
		},
		"mixed import and code": {
			head: relocationFixtureConsumerSource(
				newImport,
				alphaImport,
				"return alpha.Value() + store.Value() + 1",
				"",
			),
		},
		"unbacked module import swap": {
			head: relocationFixtureConsumerSource(
				newImport,
				relocationFixtureModule+"/internal/alpha",
				"return alpha.Value() + store.Value()",
				"",
			),
		},
		"non-module import swap": {
			head: relocationFixtureConsumerSource(
				newImport,
				externalNew,
				"return alpha.Value() + store.Value()",
				"",
			),
		},
		"build tag edit": {
			head: relocationFixtureConsumerSource(
				newImport,
				alphaImport,
				"return alpha.Value() + store.Value()",
				"linux",
			),
		},
		"declaration edit": {
			head: strings.Replace(
				relocationFixtureConsumerSource(
					newImport,
					alphaImport,
					"return alpha.Value() + store.Value()",
					"",
				),
				"func Value() int",
				"func ChangedValue() int",
				1,
			),
		},
	}

	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testBase := base
			if name == "non-module import swap" {
				testBase = relocationFixtureConsumerSource(
					oldImport,
					externalOld,
					"return alpha.Value() + store.Value()",
					"",
				)
			}
			if got := isGofmtImportOnlyRelocationChange(
				[]byte(testBase),
				[]byte(test.head),
				replacements,
			); got != test.want {
				t.Fatalf("isGofmtImportOnlyRelocationChange() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGofmtImportOnlyRelocationChangeRejectsNonCanonicalAndUnbackedSources(t *testing.T) {
	t.Parallel()
	oldImport := relocationFixtureModule + "/pkg/store"
	newImport := relocationFixtureModule + "/internal/store"
	replacements := map[string]string{oldImport: newImport}
	canonicalBase := "package consumer\n\nimport _ \"" + oldImport + "\"\n"
	canonicalHead := "package consumer\n\nimport _ \"" + newImport + "\"\n"

	tests := map[string]struct {
		base string
		head string
	}{
		"unchanged": {
			base: canonicalBase,
			head: canonicalBase,
		},
		"malformed base": {
			base: "package consumer\nimport (",
			head: canonicalHead,
		},
		"declaration fragment": {
			base: "import _ \"" + oldImport + "\"\n",
			head: "import _ \"" + newImport + "\"\n",
		},
		"non-gofmt base": {
			base: "package consumer\n\nimport  _  \"" + oldImport + "\"\n",
			head: canonicalHead,
		},
		"malformed head": {
			base: canonicalBase,
			head: "package consumer\nimport (",
		},
		"non-gofmt head": {
			base: canonicalBase,
			head: "package consumer\n\nimport  _  \"" + newImport + "\"\n",
		},
		"no backed import": {
			base: "package consumer\n\nfunc Value() int { return 1 }\n",
			head: "package consumer\n\nfunc Value() int { return 2 }\n",
		},
		"raw import literal": {
			base: "package consumer\n\nimport _ `" + oldImport + "`\n",
			head: canonicalHead,
		},
		"import alias edit": {
			base: "package consumer\n\nimport old \"" + oldImport + "\"\n",
			head: "package consumer\n\nimport changed \"" + newImport + "\"\n",
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if isGofmtImportOnlyRelocationChange(
				[]byte(test.base),
				[]byte(test.head),
				replacements,
			) {
				t.Fatal("non-import-only source pair was accepted")
			}
		})
	}

	baseWithText := "package consumer\n\nimport _ \"" + oldImport + "\"\n\n" +
		"// " + oldImport + " stays in comments.\n" +
		"const documentation = \"" + oldImport + "\"\n"
	headWithText := "package consumer\n\nimport _ \"" + newImport + "\"\n\n" +
		"// " + oldImport + " stays in comments.\n" +
		"const documentation = \"" + oldImport + "\"\n"
	if !isGofmtImportOnlyRelocationChange(
		[]byte(baseWithText),
		[]byte(headWithText),
		replacements,
	) {
		t.Fatal("token-scoped import rewrite did not preserve identical comment and string text")
	}
}

func TestModulePathFromGoModIsStrict(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		contents string
		want     string
	}{
		"ordinary": {contents: "module example.com/ordinary\n", want: "example.com/ordinary"},
		"quoted":   {contents: "module \"example.com/quoted\"\n", want: "example.com/quoted"},
		"missing":  {contents: "go 1.25\n"},
		"invalid":  {contents: "module example.com/back\\slash\n"},
		"extra":    {contents: "module example.com/too many fields\n"},
	} {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := modulePathFromGoMod([]byte(test.contents)); got != test.want {
				t.Fatalf("modulePathFromGoMod() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCoverageRelocationGitHelpersReportInvalidRepositoryState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := verifiedInternalPackageRelocationChanges(root, "base", "head"); err == nil {
		t.Fatal("relocation classifier accepted a non-repository")
	}
	if _, err := coverageComparisonBase(root, "base", "head"); err == nil {
		t.Fatal("coverage comparison base accepted a non-repository")
	}
	if _, err := gitFileAtRef(root, "missing", "go.mod"); err == nil {
		t.Fatal("git file reader accepted a non-repository")
	}
	if _, _, err := gitFilePairAtRefs(root, "base", "head", "consumer.go"); err == nil {
		t.Fatal("paired Git file reader accepted a non-repository")
	}
	if _, err := trackedPackageFiles(root, "missing", "pkg/store"); err == nil {
		t.Fatal("package tree reader accepted a non-repository")
	}
	relocation := &internalPackageRelocation{
		SourceDir:      "pkg/store",
		DestinationDir: "internal/store",
		Renames:        map[string]string{"pkg/store/store.go": "internal/store/store.go"},
	}
	if _, err := verifyInternalPackageRelocation(root, "base", "head", relocation); err == nil {
		t.Fatal("relocation verifier accepted a non-repository")
	}

	fixture := newInternalRelocationFixture(t)
	if _, _, err := gitFilePairAtRefs(
		fixture.Root,
		fixture.Base,
		"missing-head",
		"pkg/consumer/consumer.go",
	); err == nil {
		t.Fatal("paired Git file reader accepted a missing head ref")
	}
	baseSource, headSource, err := gitFilePairAtRefs(
		fixture.Root,
		fixture.Base,
		fixture.Base,
		"pkg/consumer/consumer.go",
	)
	if err != nil || !bytes.Equal(baseSource, headSource) {
		t.Fatalf("paired Git file reader = equal %v, error %v", bytes.Equal(baseSource, headSource), err)
	}
	if _, err := verifyInternalPackageRelocation(
		fixture.Root,
		fixture.Base,
		"missing-head",
		relocation,
	); err == nil {
		t.Fatal("relocation verifier accepted a missing head ref")
	}
}

func TestInternalPackageRelocationRecordRequiresExactR100PrefixRewrite(t *testing.T) {
	t.Parallel()
	valid := changedFileStatus{
		Status: "R100",
		Kind:   'R',
		Paths:  []string{"pkg/store/store.go", "internal/store/store.go"},
	}
	if source, destination, ok := internalPackageRelocationRecord(valid); !ok ||
		source != "pkg/store" || destination != "internal/store" {
		t.Fatalf("valid relocation = (%q, %q, %v)", source, destination, ok)
	}

	for _, record := range []changedFileStatus{
		{Status: "R099", Kind: 'R', Paths: valid.Paths},
		{Status: "C100", Kind: 'C', Paths: valid.Paths},
		{Status: "R100", Kind: 'R', Paths: []string{"pkg/store/store.go", "internal/other/store.go"}},
		{Status: "R100", Kind: 'R', Paths: []string{"pkg/store/store.go", "internal/store/renamed.go"}},
		{Status: "R100", Kind: 'R', Paths: []string{"third_party/store/store.go", "internal/store/store.go"}},
	} {
		if source, destination, ok := internalPackageRelocationRecord(record); ok {
			t.Errorf("invalid relocation %#v accepted as (%q, %q)", record, source, destination)
		}
	}
	relocation := &internalPackageRelocation{
		SourceDir:      "pkg/store",
		DestinationDir: "internal/store",
	}
	if internalPackageRelocationMember(
		changedFileStatus{
			Status: "R100",
			Kind:   'R',
			Paths:  []string{"pkg/other/file.go", "internal/other/file.go"},
		},
		relocation,
	) {
		t.Fatal("unrelated exact rename accepted as relocation tree member")
	}
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
	if coverageGoTestCount != 1 {
		t.Fatalf("coverage Go test count = %d, want 1", coverageGoTestCount)
	}
	if coverageGoTestParallelism != 1 {
		t.Fatalf("coverage Go test parallelism = %d, want 1", coverageGoTestParallelism)
	}
	if coverageGoMaxProcs != 2 {
		t.Fatalf("coverage Go max procs = %d, want 2", coverageGoMaxProcs)
	}
}

func TestRunCoveragePairUsesBarrierAndCleansAfterBothCollectors(t *testing.T) {
	base := preparedCoverageRef{label: "base", ref: "base-ref"}
	head := preparedCoverageRef{label: "head", ref: "head-ref"}
	started := make(chan string, 2)
	completed := make(chan string, 2)
	release := make(chan struct{})
	type pairResult struct {
		base coverageProfile
		head coverageProfile
		err  error
	}
	returned := make(chan pairResult, 1)
	cleanupBeforeJoin := make(chan string, 2)
	var cleanupOrder []string
	go func() {
		baseProfile, headProfile, err := runCoveragePair(
			base,
			head,
			func(prepared preparedCoverageRef) (coverageProfile, error) {
				started <- prepared.label
				<-release
				completed <- prepared.label
				covered := 1
				if prepared.label == "head" {
					covered = 2
				}
				return coverageProfile{Global: coverageSummary{
					CoveredStatements: covered,
					TotalStatements:   2,
				}}, nil
			},
			func(prepared preparedCoverageRef) error {
				if len(completed) != 2 {
					cleanupBeforeJoin <- prepared.label
				}
				cleanupOrder = append(cleanupOrder, prepared.label)
				return nil
			},
		)
		returned <- pairResult{base: baseProfile, head: headProfile, err: err}
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case label := <-started:
			seen[label] = true
		case <-timer.C:
			close(release)
			<-returned
			t.Fatalf("collectors did not reach barrier concurrently; started = %#v", seen)
		}
	}
	select {
	case result := <-returned:
		close(release)
		t.Fatalf("runCoveragePair returned before barrier release: %+v", result)
	default:
	}
	close(release)

	var result pairResult
	select {
	case result = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("runCoveragePair did not return after barrier release")
	}
	if result.err != nil {
		t.Fatalf("runCoveragePair() error = %v", result.err)
	}
	if result.base.Global.CoveredStatements != 1 || result.head.Global.CoveredStatements != 2 {
		t.Fatalf(
			"runCoveragePair() profiles = (%+v, %+v), want ordered base/head profiles",
			result.base.Global,
			result.head.Global,
		)
	}
	select {
	case label := <-cleanupBeforeJoin:
		t.Fatalf("cleanup for %s ran before both collectors completed", label)
	default:
	}
	if want := []string{"base", "head"}; !reflect.DeepEqual(cleanupOrder, want) {
		t.Fatalf("cleanup order = %#v, want %#v", cleanupOrder, want)
	}
}

func TestRunCoveragePairJoinsErrorsInBaseBeforeHeadOrder(t *testing.T) {
	baseErr := errors.New("base failure")
	headErr := errors.New("head failure")
	baseStarted := make(chan struct{})
	headFinished := make(chan struct{})
	releaseBase := make(chan struct{})
	result := make(chan error, 1)
	cleanupStarted := make(chan string, 2)
	go func() {
		_, _, err := runCoveragePair(
			preparedCoverageRef{label: "base"},
			preparedCoverageRef{label: "head"},
			func(prepared preparedCoverageRef) (coverageProfile, error) {
				if prepared.label == "base" {
					close(baseStarted)
					<-releaseBase
					return coverageProfile{}, baseErr
				}
				close(headFinished)
				return coverageProfile{}, headErr
			},
			func(prepared preparedCoverageRef) error {
				cleanupStarted <- prepared.label
				return nil
			},
		)
		result <- err
	}()

	for name, signal := range map[string]<-chan struct{}{
		"base start":  baseStarted,
		"head finish": headFinished,
	} {
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			close(releaseBase)
			<-result
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	select {
	case err := <-result:
		close(releaseBase)
		t.Fatalf("runCoveragePair returned before blocked base collector joined: %v", err)
	default:
	}
	select {
	case label := <-cleanupStarted:
		close(releaseBase)
		<-result
		t.Fatalf("cleanup for %s started before blocked base collector joined", label)
	default:
	}
	close(releaseBase)

	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("runCoveragePair did not join blocked base collector")
	}
	if !errors.Is(err, baseErr) || !errors.Is(err, headErr) {
		t.Fatalf("runCoveragePair() error = %v, want both base and head errors", err)
	}
	if got, want := err.Error(), "base failure\nhead failure"; got != want {
		t.Fatalf("runCoveragePair() error = %q, want deterministic %q", got, want)
	}
	var cleanupOrder []string
	for range 2 {
		cleanupOrder = append(cleanupOrder, <-cleanupStarted)
	}
	if want := []string{"base", "head"}; !reflect.DeepEqual(cleanupOrder, want) {
		t.Fatalf("error cleanup order = %#v, want %#v", cleanupOrder, want)
	}
}

func TestRunCoverageDeltaCollectsTinyRefsAndCleansWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	runCoverageDeltaTestGit(t, root, "init", "--quiet")
	runCoverageDeltaTestGit(t, root, "config", "user.email", "coverage-pair@example.invalid")
	runCoverageDeltaTestGit(t, root, "config", "user.name", "Coverage Pair Test")
	runCoverageDeltaTestGit(t, root, "config", "commit.gpgSign", "false")
	runCoverageDeltaTestGit(t, root, "config", "core.hooksPath", t.TempDir())

	for path, contents := range map[string]string{
		"go.mod":               "module example.com/coveragepair\n\ngo 1.25\n",
		"cmd/picoclaw/main.go": "package main\n\nfunc main() {}\n",
		"pkg/sample/sample.go": "package sample\n\nfunc Value() int { return 1 }\n",
		"pkg/sample/sample_test.go": `package sample

import "testing"

func TestValue(t *testing.T) {
	if Value() != 1 {
		t.Fatal("unexpected value")
	}
}
`,
		"docs/features/sample.md": "# Sample\n\nFR-SAMPLE\n\nOwns: CODE pkg/sample/**\nOwns: TEST pkg/sample/*\n",
	} {
		writeCoverageDeltaTestFile(t, root, path, contents)
	}
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD"))

	writeCoverageDeltaTestFile(t, root, "pkg/sample/sample.go", "package sample\n\nfunc Value() int { return 2 }\n")
	writeCoverageDeltaTestFile(t, root, "pkg/sample/sample_test.go", `package sample

import "testing"

func TestValue(t *testing.T) {
	if Value() != 2 {
		t.Fatal("unexpected value")
	}
}
`)
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "head")
	head := strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD"))

	if err := runCoverageDelta(root, base, head, "", nil, false); err != nil {
		t.Fatalf("runCoverageDelta() error = %v", err)
	}
	worktrees := runCoverageDeltaTestGit(t, root, "worktree", "list", "--porcelain")
	if got := strings.Count(worktrees, "worktree "); got != 1 {
		t.Fatalf("worktree count after coverage = %d, want 1\n%s", got, worktrees)
	}
}

func TestRunCoveragePairReportsCleanupErrors(t *testing.T) {
	baseCleanupErr := errors.New("base cleanup failure")
	headCleanupErr := errors.New("head cleanup failure")
	_, _, err := runCoveragePair(
		preparedCoverageRef{label: "base"},
		preparedCoverageRef{label: "head"},
		func(preparedCoverageRef) (coverageProfile, error) {
			return emptyCoverageProfile(), nil
		},
		func(prepared preparedCoverageRef) error {
			if prepared.label == "base" {
				return baseCleanupErr
			}
			return headCleanupErr
		},
	)
	if !errors.Is(err, baseCleanupErr) || !errors.Is(err, headCleanupErr) {
		t.Fatalf("runCoveragePair() cleanup error = %v, want both cleanup errors", err)
	}
	if got, want := err.Error(), "base cleanup failure\nhead cleanup failure"; got != want {
		t.Fatalf("runCoveragePair() cleanup error = %q, want %q", got, want)
	}
}

func TestCoverageWorktreeCleanupHandlesReadOnlyModuleDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	runCoverageDeltaTestGit(t, root, "init", "--quiet")
	runCoverageDeltaTestGit(t, root, "config", "user.email", "coverage-cleanup@example.invalid")
	runCoverageDeltaTestGit(t, root, "config", "user.name", "Coverage Cleanup Test")
	writeCoverageDeltaTestFile(t, root, "go.mod", "module example.com/cleanup\n\ngo 1.25\n")
	runCoverageDeltaTestGit(t, root, "add", "--all")
	runCoverageDeltaTestGit(t, root, "commit", "--quiet", "-m", "base")
	ref := strings.TrimSpace(runCoverageDeltaTestGit(t, root, "rev-parse", "HEAD"))
	temporaryRoot := t.TempDir()
	prepared, err := prepareCoverageRef(
		root,
		temporaryRoot,
		"base",
		ref,
		os.Environ(),
		goCachePaths{Build: t.TempDir(), Modules: t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(prepared.worktree, ".cache", "go-mod", "example@v1.0.0")
	if err := os.MkdirAll(readOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCoverageDeltaTestFile(t, readOnly, "module.go", "package module\n")
	if err := os.Chmod(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := cleanupCoverageRefs(root, []preparedCoverageRef{prepared}); err != nil {
		t.Fatalf("cleanupCoverageRefs() error = %v", err)
	}
	if _, err := os.Stat(prepared.worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleaned worktree stat error = %v, want not exist", err)
	}
	if err := makeCoverageWorktreeRemovable(filepath.Join(temporaryRoot, "missing")); err != nil {
		t.Fatalf("makeCoverageWorktreeRemovable(missing) error = %v", err)
	}
	if _, err := prepareCoverageRef(
		root,
		temporaryRoot,
		"missing",
		"missing-ref",
		os.Environ(),
		goCachePaths{Build: t.TempDir(), Modules: t.TempDir()},
	); err == nil {
		t.Fatal("prepareCoverageRef accepted missing ref")
	}
}

func TestRunIntegrationCoveragePassesParallelRefIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	parent := t.TempDir()
	worktree := filepath.Join(parent, "head")
	for path, contents := range map[string]string{
		"go.mod": "module example.com/integrationcoverage\n\ngo 1.25\n",
		"pkg/sample/sample.go": `package sample

func Value() int { return 1 }
`,
		"scripts/run-integration-tests.sh": `#!/usr/bin/env bash
set -euo pipefail
[[ "${INTEGRATION_COMPOSE_PROJECT_NAMESPACE}" == "${EXPECTED_NAMESPACE}" ]]
[[ "${INTEGRATION_GOMAXPROCS}" == "2" ]]
[[ "${INTEGRATION_GOCACHE}" == "${EXPECTED_GOCACHE}" ]]
[[ "${INTEGRATION_GOMODCACHE}" == "${EXPECTED_GOMODCACHE}" ]]
[[ "${INTEGRATION_RUNNER_UID}" == "34567" ]]
[[ "${INTEGRATION_RUNNER_GID}" == "45678" ]]
[[ "${GOFLAGS}" == "-tags=goolm,stdjson,integration" ]]
[[ "$#" == 1 && "$1" == "sample-suite" ]]
mkdir -p .coverage/integration-head
printf '%s\n' \
  'mode: atomic' \
  'example.com/integrationcoverage/pkg/sample/sample.go:3.18,3.28 1 1' \
  >.coverage/integration-head/sample.cover.out
`,
	} {
		writeCoverageDeltaTestFile(t, worktree, path, contents)
	}
	if err := os.Chmod(filepath.Join(worktree, "scripts", "run-integration-tests.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	expectedNamespace := filepath.Base(parent) + "-head"
	cachePaths := goCachePaths{
		Build:   filepath.Join(parent, "shared-cache", "build"),
		Modules: filepath.Join(parent, "shared-cache", "modules"),
	}
	environment := coverageEnvironment([]string{
		"PATH=" + os.Getenv("PATH"),
		"INTEGRATION_RUNNER_UID=34567",
		"INTEGRATION_RUNNER_GID=45678",
	}, filepath.Join(parent, "coverage-home"), cachePaths)
	environment = append(
		environment,
		"EXPECTED_NAMESPACE="+expectedNamespace,
		"EXPECTED_GOCACHE="+cachePaths.Build,
		"EXPECTED_GOMODCACHE="+cachePaths.Modules,
	)
	profile, err := runIntegrationCoverage(
		worktree,
		"head",
		"head-ref",
		"goolm,stdjson",
		[]string{"example.com/integrationcoverage/pkg/sample"},
		[]string{"sample-suite"},
		environment,
	)
	if err != nil {
		t.Fatalf("runIntegrationCoverage() error = %v", err)
	}
	if profile.Global != (coverageSummary{CoveredStatements: 1, TotalStatements: 1}) {
		t.Fatalf("integration profile global = %+v, want 1/1", profile.Global)
	}
}

func TestCreateCoverageTemporaryRootUsesShortPrefixAndReportsFailure(t *testing.T) {
	parent := t.TempDir()
	root, err := createCoverageTemporaryRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if filepath.Dir(root) != parent || !strings.HasPrefix(filepath.Base(root), "pc-") {
		t.Fatalf("coverage temporary root = %q, want short pc- child of %q", root, parent)
	}

	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err = os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if root, err = createCoverageTemporaryRoot(blockedParent); err == nil || root != "" {
		t.Fatalf("blocked coverage temporary root = (%q, %v), want empty path and error", root, err)
	}
}

func TestRunGoCoverageUsesFreshSerializedExecution(t *testing.T) {
	root := t.TempDir()
	writeScriptCoverageFixture(t, root, "go.mod", "module example.com/coveragefixture\n\ngo 1.24\n")
	writeScriptCoverageFixture(
		t,
		root,
		"sample/sample.go",
		"package sample\n\nfunc Value() int { return 42 }\n",
	)
	writeScriptCoverageFixture(
		t,
		root,
		"sample/sample_test.go",
		"package sample\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 42 { t.Fatal(\"wrong value\") } }\n",
	)
	profilePath := filepath.Join(root, "coverage.out")
	profile, err := runGoCoverage(
		root,
		"head",
		"fixture",
		"",
		profilePath,
		[]string{"example.com/coveragefixture/sample"},
		[]string{"example.com/coveragefixture/sample"},
		os.Environ(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.Files["sample/sample.go"]; got.TotalStatements == 0 || got.CoveredStatements == 0 {
		t.Fatalf("fixture coverage = %+v, want covered statements", got)
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

func relocationComparisonProfile(
	movedPath string,
	movedCovered, consumerCovered bool,
) coverageProfile {
	profile := emptyCoverageProfile()
	addCoverageBlock(profile, coverageBlock{
		File: movedPath, Range: "3.20,3.28", StartLine: 3, StartCol: 20,
		EndLine: 3, EndCol: 28, Statements: 2, Covered: movedCovered,
	})
	addCoverageBlock(profile, coverageBlock{
		File: "pkg/consumer/consumer.go", Range: "8.2,8.24", StartLine: 8, StartCol: 2,
		EndLine: 8, EndCol: 24, Statements: 3, Covered: consumerCovered,
	})
	return summarizeCoverageBlocks(profile)
}

func TestCompareCoverageWaivesOnlyGlobalNoiseForVerifiedRelocation(t *testing.T) {
	t.Parallel()
	const source = "pkg/store/store.go"
	const destination = "internal/store/store.go"
	spec := featureSpecMetadata{
		RelPath: "docs/features/store.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/store/**"},
			{Kind: "CODE", Pattern: "internal/store/**"},
		},
	}
	plan := coveragePlan{
		ImpactedFeature: map[string]bool{spec.RelPath: true},
		RelocatedFiles:  map[string]string{source: destination},
	}
	base := relocationComparisonProfile(source, true, true)
	head := relocationComparisonProfile(destination, true, false)
	wantBase := relocationComparisonProfile(source, true, true)
	wantHead := relocationComparisonProfile(destination, true, false)

	if failures := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); len(failures) != 0 {
		t.Fatalf("covered-bit-only relocation failures = %#v", failures)
	}
	if !reflect.DeepEqual(base, wantBase) || !reflect.DeepEqual(head, wantHead) {
		t.Fatal("relocation comparison mutated a coverage profile")
	}

	head = relocationComparisonProfile(destination, false, true)
	want := []string{
		"docs/features/store.md Go coverage regressed: uncovered statement debt 0 -> 2 and coverage 100.00% (2/2) -> 0.00% (0/2)",
	}
	if got := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); !reflect.DeepEqual(got, want) {
		t.Fatalf("moved-owner failures = %#v, want %#v", got, want)
	}
}

func TestCompareCoverageRelocationWaiverRequiresNoChangedExecutableStatements(t *testing.T) {
	t.Parallel()
	const source = "pkg/store/store.go"
	const destination = "internal/store/store.go"
	base := relocationComparisonProfile(source, true, true)
	head := relocationComparisonProfile(destination, true, false)
	plan := coveragePlan{
		RelocatedFiles: map[string]string{source: destination},
		ChangedLines: map[string]map[int]bool{
			destination: {3: true},
		},
	}
	want := []string{
		"scoped Go coverage regressed: uncovered statement debt 0 -> 3 and coverage 100.00% (5/5) -> 40.00% (2/5)",
	}
	if got := compareCoverage(nil, plan, base, head); !reflect.DeepEqual(got, want) {
		t.Fatalf("covered changed-block failures = %#v, want %#v", got, want)
	}

	plan.ChangedLines = map[string]map[int]bool{
		"pkg/consumer/consumer.go": {8: true},
	}
	want = append(want, "changed production Go coverage is below 90%: 0.00% (0/3)")
	if got := compareCoverage(nil, plan, base, head); !reflect.DeepEqual(got, want) {
		t.Fatalf("uncovered changed-block failures = %#v, want %#v", got, want)
	}
}

func TestRelocationCoverageStructureAcceptsCompilerZeroStatementBlocks(t *testing.T) {
	t.Parallel()
	const source = "pkg/store/store.go"
	const destination = "internal/store/store.go"
	profile := func(path string, covered bool) coverageProfile {
		result := emptyCoverageProfile()
		addCoverageBlock(result, coverageBlock{
			File: path, Range: "519.19,519.19", StartLine: 519, StartCol: 19,
			EndLine: 519, EndCol: 19, Statements: 0, Covered: covered,
		})
		return summarizeCoverageBlocks(result)
	}
	base := profile(source, true)
	head := profile(destination, false)
	relocations := map[string]string{source: destination}
	if err := relocationCoverageStructureMatches(base, head, relocations); err != nil {
		t.Fatalf("zero-statement relocation block mismatch: %v", err)
	}
	if failures := compareCoverage(
		nil,
		coveragePlan{RelocatedFiles: relocations},
		base,
		head,
	); len(failures) != 0 {
		t.Fatalf("zero-statement relocation failures = %#v", failures)
	}

	invalid := profile(source, false)
	block := invalid.Blocks[source]["519.19,519.19"]
	block.Statements = -1
	invalid.Blocks[source][block.Range] = block
	invalid.Global.TotalStatements = -1
	if err := relocationCoverageStructureMatches(invalid, head, relocations); err == nil {
		t.Fatal("negative-statement relocation block was accepted")
	}
}

func TestRelocationCoverageStructureFailsClosed(t *testing.T) {
	t.Parallel()
	const source = "pkg/store/store.go"
	const destination = "internal/store/store.go"
	baseProfile := func() coverageProfile {
		return relocationComparisonProfile(source, true, true)
	}
	headProfile := func() coverageProfile {
		return relocationComparisonProfile(destination, false, false)
	}

	tests := map[string]struct {
		base        func() coverageProfile
		head        func() coverageProfile
		relocations map[string]string
	}{
		"missing block": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				delete(profile.Blocks, "pkg/consumer/consumer.go")
				return summarizeCoverageBlocks(profile)
			},
			relocations: map[string]string{source: destination},
		},
		"extra block": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				addCoverageBlock(profile, coverageBlock{
					File: "pkg/consumer/consumer.go", Range: "12.2,12.12",
					StartLine: 12, StartCol: 2, EndLine: 12, EndCol: 12,
					Statements: 1,
				})
				return summarizeCoverageBlocks(profile)
			},
			relocations: map[string]string{source: destination},
		},
		"range": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				block := profile.Blocks[destination]["3.20,3.28"]
				delete(profile.Blocks[destination], "3.20,3.28")
				block.Range = "3.20,3.29"
				block.EndCol = 29
				profile.Blocks[destination][block.Range] = block
				return summarizeCoverageBlocks(profile)
			},
			relocations: map[string]string{source: destination},
		},
		"columns": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				block := profile.Blocks[destination]["3.20,3.28"]
				block.StartCol = 19
				profile.Blocks[destination][block.Range] = block
				return profile
			},
			relocations: map[string]string{source: destination},
		},
		"block map identity": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				block := profile.Blocks[destination]["3.20,3.28"]
				block.File = "internal/store/other.go"
				profile.Blocks[destination][block.Range] = block
				return profile
			},
			relocations: map[string]string{source: destination},
		},
		"statements": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				block := profile.Blocks[destination]["3.20,3.28"]
				block.Statements++
				profile.Blocks[destination][block.Range] = block
				return summarizeCoverageBlocks(profile)
			},
			relocations: map[string]string{source: destination},
		},
		"file": {
			base: baseProfile,
			head: func() coverageProfile {
				profile := headProfile()
				blocks := profile.Blocks[destination]
				delete(profile.Blocks, destination)
				block := blocks["3.20,3.28"]
				block.File = "internal/store/other.go"
				profile.Blocks[block.File] = map[string]coverageBlock{block.Range: block}
				return summarizeCoverageBlocks(profile)
			},
			relocations: map[string]string{source: destination},
		},
		"destination represented in base": {
			base: func() coverageProfile {
				profile := baseProfile()
				profile.Blocks[destination] = map[string]coverageBlock{}
				return profile
			},
			head:        headProfile,
			relocations: map[string]string{source: destination},
		},
		"base statement total mismatch": {
			base: func() coverageProfile {
				profile := baseProfile()
				profile.Global.TotalStatements++
				return profile
			},
			head:        headProfile,
			relocations: map[string]string{source: destination},
		},
		"non-injective map": {
			base: baseProfile,
			head: headProfile,
			relocations: map[string]string{
				source:               destination,
				"pkg/other/other.go": destination,
			},
		},
		"destination is source": {
			base: baseProfile,
			head: headProfile,
			relocations: map[string]string{
				source:      destination,
				destination: "internal/store/final.go",
			},
		},
		"same path": {
			base:        baseProfile,
			head:        headProfile,
			relocations: map[string]string{source: source},
		},
		"empty path": {
			base:        baseProfile,
			head:        headProfile,
			relocations: map[string]string{"": destination},
		},
		"empty map": {
			base:        baseProfile,
			head:        headProfile,
			relocations: map[string]string{},
		},
	}

	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := test.base()
			head := test.head()
			if err := relocationCoverageStructureMatches(base, head, test.relocations); err == nil {
				t.Fatal("mismatched relocation coverage structure was accepted")
			}
			if len(test.relocations) == 0 {
				return
			}
			plan := coveragePlan{RelocatedFiles: test.relocations}
			failures := compareCoverage(nil, plan, base, head)
			if !slices.ContainsFunc(failures, func(failure string) bool {
				return strings.HasPrefix(
					failure,
					"verified internal package relocation coverage structure mismatch:",
				)
			}) {
				t.Fatalf("compareCoverage failures = %#v, want explicit structure mismatch", failures)
			}
		})
	}
}

func TestCompareCoverageRelocationDoesNotBypassNewScopeThreshold(t *testing.T) {
	t.Parallel()
	base := emptyCoverageProfile()
	head := emptyCoverageProfile()
	addCoverageBlock(head, coverageBlock{
		File: "internal/store/store.go", Range: "3.20,3.28",
		StartLine: 3, StartCol: 20, EndLine: 3, EndCol: 28, Statements: 100,
		Covered: false,
	})
	head = summarizeCoverageBlocks(head)
	plan := coveragePlan{
		RelocatedFiles: map[string]string{
			"pkg/store/store.go": "internal/store/store.go",
		},
	}
	failures := compareCoverage(nil, plan, base, head)
	if !slices.Contains(
		failures,
		"scoped new Go coverage is below 95%: 0.00% (0/100)",
	) {
		t.Fatalf("new-scope relocation failures = %#v, want 95%% threshold failure", failures)
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
		"GOMAXPROCS=64",
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
		"INTEGRATION_GOCACHE=/operator/go-build-cache",
		"INTEGRATION_GOMODCACHE=/operator/go-mod-cache",
		"INTEGRATION_RUNNER_UID=34567",
		"INTEGRATION_RUNNER_GID=45678",
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
	assertEnvironmentValue(t, baseEnvironment, "TMPDIR", "/isolated/base-tmp")
	assertEnvironmentValue(t, baseEnvironment, "TEMP", "/isolated/base-tmp")
	assertEnvironmentValue(t, baseEnvironment, "TMP", "/isolated/base-tmp")
	assertEnvironmentValue(t, headEnvironment, "TMPDIR", "/isolated/head-tmp")
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
	assertEnvironmentValue(t, baseEnvironment, "GOMAXPROCS", "2")
	assertEnvironmentValue(t, baseEnvironment, "AWS_EC2_METADATA_DISABLED", "true")
	assertEnvironmentValue(t, baseEnvironment, "GIT_TERMINAL_PROMPT", "0")
	assertEnvironmentValue(t, baseEnvironment, "GCM_INTERACTIVE", "never")
	assertEnvironmentValue(t, baseEnvironment, "GOAUTH", "off")
	assertEnvironmentValue(t, baseEnvironment, "GOENV", "off")
	assertEnvironmentValue(t, baseEnvironment, "INTEGRATION_RUNNER_UID", "34567")
	assertEnvironmentValue(t, baseEnvironment, "INTEGRATION_RUNNER_GID", "45678")
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
		"INTEGRATION_GOCACHE",
		"INTEGRATION_GOMODCACHE",
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
	temporaryDirectory := coverageTemporaryDirectory(home)
	if info, err := os.Stat(temporaryDirectory); err != nil || !info.IsDir() {
		t.Fatalf("coverage temporary directory = (%v, %v), want directory", info, err)
	}
	if filepath.Dir(temporaryDirectory) != filepath.Dir(home) {
		t.Fatalf("coverage temporary directory %q is not a sibling of home %q", temporaryDirectory, home)
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
