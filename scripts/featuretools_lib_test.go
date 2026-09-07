//go:build featuretools

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseChangedFileStatusesIncludesRenameEndpointsAndCopyDestination(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"M", "./pkg/z.go",
		"A", "/pkg/a.go",
		"T", "pkg/type.go",
		"D", "pkg/deleted.go",
		"R087", "pkg/old.go", "pkg/new.go",
		"C100", "pkg/source.go", "pkg/copied.go",
		"M", "pkg/old.go",
		"",
	}, "\x00")

	records, err := parseChangedFileStatusRecords(out)
	if err != nil {
		t.Fatal(err)
	}
	wantRecords := []changedFileStatus{
		{Kind: 'M', Paths: []string{"pkg/z.go"}},
		{Kind: 'A', Paths: []string{"pkg/a.go"}},
		{Kind: 'T', Paths: []string{"pkg/type.go"}},
		{Kind: 'D', Paths: []string{"pkg/deleted.go"}},
		{Kind: 'R', Paths: []string{"pkg/old.go", "pkg/new.go"}},
		{Kind: 'C', Paths: []string{"pkg/source.go", "pkg/copied.go"}},
		{Kind: 'M', Paths: []string{"pkg/old.go"}},
	}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("parseChangedFileStatusRecords() = %#v, want %#v", records, wantRecords)
	}

	got, err := parseChangedFileStatuses(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pkg/a.go",
		"pkg/copied.go",
		"pkg/deleted.go",
		"pkg/new.go",
		"pkg/old.go",
		"pkg/type.go",
		"pkg/z.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseChangedFileStatuses() = %#v, want %#v", got, want)
	}
}

func TestParseChangedFileStatusesRejectsMalformedRecords(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not NUL terminated":  "M\x00pkg/file.go",
		"empty status":        "\x00",
		"unsupported status":  "U\x00pkg/file.go\x00",
		"missing path":        "M\x00",
		"missing rename path": "R100\x00pkg/old.go\x00",
		"empty path":          "A\x00\x00",
	}
	for name, out := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseChangedFileStatuses(out); err == nil {
				t.Fatalf("parseChangedFileStatuses(%q) unexpectedly succeeded", out)
			}
		})
	}
}

func TestChangedFileStatusErrorsAndEmptyOutput(t *testing.T) {
	t.Parallel()
	if records, err := parseChangedFileStatusRecords(""); err != nil || len(records) != 0 {
		t.Fatalf("empty change records = %#v, %v", records, err)
	}
	if _, err := changedFiles(t.TempDir(), "missing-base", "missing-head"); err == nil {
		t.Fatal("changedFiles accepted a non-repository")
	}
}

func TestChangedFilesIncludesBothRenamePaths(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Win32 filenames cannot contain the odd-path fixture characters")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	root := t.TempDir()
	runFeaturetoolsTestGit(t, root, "init", "--quiet")
	runFeaturetoolsTestGit(t, root, "config", "user.email", "featuretools@example.invalid")
	runFeaturetoolsTestGit(t, root, "config", "user.name", "Feature Tools Test")
	runFeaturetoolsTestGit(t, root, "config", "commit.gpgSign", "false")
	hooks := filepath.Join(root, "disabled-hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	runFeaturetoolsTestGit(t, root, "config", "core.hooksPath", hooks)

	oldPath := filepath.Join(root, "pkg", "old\nname.go")
	newPath := filepath.Join(root, "pkg", "new\nname.go")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFeaturetoolsTestGit(t, root, "add", "--all")
	runFeaturetoolsTestGit(t, root, "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(runFeaturetoolsTestGit(t, root, "rev-parse", "HEAD"))

	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	runFeaturetoolsTestGit(t, root, "add", "--all")
	runFeaturetoolsTestGit(t, root, "commit", "--quiet", "-m", "rename")
	head := strings.TrimSpace(runFeaturetoolsTestGit(t, root, "rev-parse", "HEAD"))

	got, err := changedFiles(root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/new\nname.go", "pkg/old\nname.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedFiles() = %#v, want %#v", got, want)
	}
}

func runFeaturetoolsTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestFrontendExpectedSpecPathsUsesConfiguredRule(t *testing.T) {
	cfg := frontendOwnershipConfig{
		Rules: []frontendOwnershipRule{
			{
				Spec: "docs/features/chat-channels.md",
				Patterns: []string{
					"web/frontend/src/components/chat/**",
				},
			},
		},
	}
	normalizeFrontendOwnershipConfig(&cfg)

	got := frontendExpectedSpecPaths(cfg, "web/frontend/src/components/chat/assistant-message.tsx")
	want := []string{"docs/features/chat-channels.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frontendExpectedSpecPaths() = %#v, want %#v", got, want)
	}
}

func TestDiscoverTestsSkipsIgnoredDocsPlansEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	productTest := filepath.Join(root, "pkg", "feature_test.go")
	plannedEvidence := filepath.Join(root, "docs", "plans", "run", "candidate_test.go")
	for _, path := range []string{productTest, plannedEvidence} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			path,
			[]byte("package fixture\nfunc TestDiscovered(t *testing.T) {}\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	discovered := make(map[string]bool)
	if err := discoverTests(root, func(_, _, source string) {
		discovered[source] = true
	}); err != nil {
		t.Fatal(err)
	}
	if !discovered["pkg/feature_test.go"] ||
		discovered["docs/plans/run/candidate_test.go"] || len(discovered) != 1 {
		t.Fatalf("discovered tests = %#v", discovered)
	}
}

func TestForbiddenFrontendCodeOwnershipPatternUsesConfiguredPatterns(t *testing.T) {
	cfg := frontendOwnershipConfig{
		ForbiddenBroadCodeOwnershipPatterns: []string{
			"web/frontend/**",
			"web/frontend/src/components/**",
		},
	}
	normalizeFrontendOwnershipConfig(&cfg)

	if !forbiddenFrontendCodeOwnershipPattern(cfg, "./web/frontend/**") {
		t.Fatal("expected broad frontend ownership pattern to be forbidden")
	}
	if forbiddenFrontendCodeOwnershipPattern(cfg, "web/frontend/src/components/chat/**") {
		t.Fatal("expected feature-owned frontend component pattern to be allowed")
	}
}

func TestExpectedOwnerSpecChangedRequiresExpectedFrontendSpec(t *testing.T) {
	expected := []string{"docs/features/chat-channels.md"}
	owners := []featureOwnership{
		{SpecRelPath: "docs/features/chat-channels.md"},
		{SpecRelPath: "docs/features/launcher-management.md"},
	}

	if expectedOwnerSpecChanged(expected, owners, map[string]bool{
		"docs/features/launcher-management.md": true,
	}) {
		t.Fatal("wrong-but-owning frontend spec satisfied expected owner check")
	}

	if !expectedOwnerSpecChanged(expected, owners, map[string]bool{
		"docs/features/chat-channels.md": true,
	}) {
		t.Fatal("expected changed frontend owner spec to satisfy expected owner check")
	}
}
