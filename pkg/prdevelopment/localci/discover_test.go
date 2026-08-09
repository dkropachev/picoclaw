package localci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDiscoverPairSeparatesDefinitionsFromDependencies(t *testing.T) {
	baseline := t.TempDir()
	candidate := t.TempDir()
	writeTestFile(t, baseline, "go.mod", "module example.test/project\n\ngo 1.25\n")
	writeTestFile(
		t,
		candidate,
		"go.mod",
		"module example.test/project\n\ngo 1.25\nrequire example.test/dependency v1.0.0\n",
	)

	resolved, err := DiscoverPair(context.Background(), baseline, candidate)
	if err != nil {
		t.Fatalf("DiscoverPair() error = %v", err)
	}
	if resolved.Changed {
		t.Fatal("dependency-only change marked the plan changed")
	}
	if !resolved.Effective.Complete || len(resolved.Effective.Steps) != 3 {
		t.Fatalf("effective plan = %#v, want complete Go lint/test/build", resolved.Effective)
	}
	if resolved.Baseline.DependencyDigest == resolved.Candidate.DependencyDigest {
		t.Fatal("dependency-only change did not invalidate dependency identity")
	}
	if resolved.Baseline.DefinitionDigest != resolved.Candidate.DefinitionDigest {
		t.Fatal("dependency-only go.mod change altered definition identity")
	}
}

func TestDiscoverPairRejectsChangedValidationDefinition(t *testing.T) {
	baseline := t.TempDir()
	candidate := t.TempDir()
	writeTestFile(t, baseline, "Makefile", "lint:\n\ttrue\n")
	writeTestFile(t, candidate, "Makefile", "lint:\n\tfalse\n")

	resolved, err := DiscoverPair(context.Background(), baseline, candidate)
	if err != nil {
		t.Fatalf("DiscoverPair() error = %v", err)
	}
	if !resolved.Changed || resolved.Effective.Complete {
		t.Fatalf("resolved plan = %#v, want changed incomplete plan", resolved)
	}
	if !hasDiagnostic(resolved.Effective.Diagnostics, "plan_changed") {
		t.Fatalf("diagnostics = %#v, want plan_changed", resolved.Effective.Diagnostics)
	}
}

func TestDiscoverDoesNotEvaluateMakefile(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	writeTestFile(t, root, "Makefile", "value := $(shell touch sentinel)\nlint:\n\t@true\n")

	plan, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !plan.Complete || len(plan.Steps) != 1 {
		t.Fatalf("plan = %#v, want one complete lint step", plan)
	}
	if _, err = os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery evaluated Makefile; sentinel stat error = %v", err)
	}
}

func TestDiscoverGitHubWorkflowStaticAndUnsupportedActions(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".github/workflows/pr.yml", `name: PR
on:
  pull_request: {}
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - name: lint
        run: go vet ./...
`)
	plan, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !plan.Complete || len(plan.Steps) != 1 || plan.Steps[0].Script != "go vet ./..." {
		t.Fatalf("static workflow plan = %#v", plan)
	}

	writeTestFile(t, root, ".github/workflows/pr.yml", `name: PR
on:
  pull_request: {}
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: owner/arbitrary-action@0123456789012345678901234567890123456789
`)
	plan, err = Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover(unsupported action) error = %v", err)
	}
	if plan.Complete || !hasDiagnostic(plan.Diagnostics, "unsupported_action") {
		t.Fatalf("unsupported-action plan = %#v, want incomplete", plan)
	}
}

func TestDiscoverRejectsGitHubJobThatNeedsSharedStepState(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".github/workflows/pr.yml", `name: PR
on:
  pull_request: {}
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: printf state > generated
      - run: test -f generated
`)

	plan, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if plan.Complete || len(plan.Steps) != 0 ||
		!hasDiagnostic(plan.Diagnostics, "stateful_job_unsupported") {
		t.Fatalf("stateful workflow plan = %#v, want incomplete and unexecutable", plan)
	}
}

func TestDiscoverRejectsUnmodeledGitHubSetupAndCheckoutInputs(t *testing.T) {
	for _, workflow := range []string{
		`name: PR
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@0123456789012345678901234567890123456789
      - run: go test ./...
`,
		`name: PR
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
        with:
          ref: other-branch
      - run: go test ./...
`,
	} {
		root := t.TempDir()
		writeTestFile(t, root, ".github/workflows/pr.yml", workflow)
		plan, err := Discover(context.Background(), root)
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if plan.Complete || !hasDiagnostic(plan.Diagnostics, "unsupported_action") &&
			!hasDiagnostic(plan.Diagnostics, "unsupported_action_input") {
			t.Fatalf("unmodeled action plan = %#v, want incomplete", plan)
		}
	}
}

func TestDiscoverRejectsReservedEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", `version: 1
steps:
  - id: unsafe
    name: Unsafe override
    kind: test
    command: [true]
    environment:
      GIT_CONFIG_COUNT: "1"
`)

	if _, err := Discover(context.Background(), root); err == nil {
		t.Fatal("Discover(reserved environment override) error = nil")
	}
}

func TestDiscoverExplicitPlanAndStrictYAML(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", `version: 1
steps:
  - id: lint
    name: Focused lint
    kind: lint
    command: [go, vet, ./...]
    timeout-seconds: 30
`)
	plan, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !plan.Complete || len(plan.Steps) != 1 || plan.Steps[0].ID != "lint" {
		t.Fatalf("explicit plan = %#v", plan)
	}

	writeTestFile(t, root, ".picoclaw/ci.yml", "version: 1\nversion: 1\nsteps: []\n")
	if _, err = Discover(context.Background(), root); err == nil {
		t.Fatal("Discover(duplicate YAML key) error = nil")
	}
}

func TestDiscoverRejectsSymlinkedAndFIFOInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and FIFO semantics differ on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Makefile")
	if err := os.WriteFile(outside, []byte("lint:\n\ttrue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), root); err == nil {
		t.Fatal("Discover(symlinked Makefile) error = nil")
	}
	if err := os.Remove(filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if err := syscallMkfifo(filepath.Join(root, "Makefile")); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Discover(ctx, root); err == nil {
		t.Fatal("Discover(FIFO Makefile) error = nil")
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
