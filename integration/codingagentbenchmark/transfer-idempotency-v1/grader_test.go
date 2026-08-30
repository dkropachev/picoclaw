package transferidempotencybenchmark

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
)

type benchmarkStep struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Kind           string   `yaml:"kind"`
	Run            string   `yaml:"run"`
	Command        []string `yaml:"command"`
	Shell          string   `yaml:"shell"`
	TimeoutSeconds int64    `yaml:"timeout-seconds"`
}

type benchmarkManifest struct {
	Version      int    `yaml:"version"`
	ID           string `yaml:"id"`
	ModelProfile struct {
		ReasoningEffort string `yaml:"reasoning-effort"`
	} `yaml:"model-profile"`
	LocalCI struct {
		Definition string          `yaml:"definition"`
		Steps      []benchmarkStep `yaml:"steps"`
	} `yaml:"local-ci"`
}

type graderArtifact struct {
	Version       int      `json:"version"`
	Fixture       string   `json:"fixture"`
	Score         int      `json:"score"`
	MandatoryPass bool     `json:"mandatory_pass"`
	ChangedFiles  []string `json:"changed_files"`
	Checks        struct {
		Format       bool `json:"format"`
		Vet          bool `json:"vet"`
		Test         bool `json:"test"`
		Race         bool `json:"race"`
		Scope        bool `json:"scope"`
		GitHead      bool `json:"git_head"`
		TestsChanged bool `json:"tests_changed"`
	} `json:"checks"`
	Mutation struct {
		Killed  int `json:"killed"`
		Total   int `json:"total"`
		Points  int `json:"points"`
		Mutants []struct {
			ID     string `json:"id"`
			Killed bool   `json:"killed"`
		} `json:"mutants"`
	} `json:"mutation"`
}

func TestFixtureDeclaresExactSandboxedLocalCISteps(t *testing.T) {
	root := fixtureRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "benchmark.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest benchmarkManifest
	if err = yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse benchmark.yaml: %v", err)
	}
	if manifest.Version != 2 || manifest.ID != "transfer-idempotency-v1" ||
		manifest.ModelProfile.ReasoningEffort != "low" ||
		manifest.LocalCI.Definition != ".picoclaw/ci.yaml" {
		t.Fatalf("benchmark manifest identity = %#v", manifest)
	}

	plan, err := localci.Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !plan.Complete || len(plan.Steps) != 4 || len(manifest.LocalCI.Steps) != 4 {
		t.Fatalf("LocalCI plans = manifest %#v, discovered %#v", manifest.LocalCI.Steps, plan)
	}
	for index, declared := range manifest.LocalCI.Steps {
		discovered := plan.Steps[index]
		if declared.ID != discovered.ID || declared.Name != discovered.Name ||
			declared.Kind != string(discovered.Kind) || declared.Run != discovered.Script ||
			declared.Shell != discovered.Shell || declared.TimeoutSeconds != discovered.TimeoutSeconds ||
			!reflect.DeepEqual(declared.Command, discovered.Argv) {
			t.Fatalf("LocalCI step %d mismatch: manifest %#v, discovered %#v", index, declared, discovered)
		}
	}
	wantIDs := []string{"format", "vet", "test", "race"}
	for index, want := range wantIDs {
		if plan.Steps[index].ID != want {
			t.Fatalf("LocalCI step %d ID = %q, want %q", index, plan.Steps[index].ID, want)
		}
	}
}

func TestFixtureExcludesHiddenEvaluationAssets(t *testing.T) {
	root := fixtureRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(filepath.ToSlash(relative))
		if strings.Contains(lower, "hidden") || strings.Contains(lower, "mutant") ||
			strings.Contains(lower, "testdata") || strings.HasSuffix(lower, "grade.sh") {
			return errors.New("model-visible fixture contains evaluation asset: " + relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHiddenSuiteKillsEveryFixedMutant(t *testing.T) {
	requireBenchmarkTools(t)
	graderRoot := graderRoot(t)
	mutantsRoot := filepath.Join(graderRoot, "testdata", "mutants")
	entries, err := os.ReadDir(mutantsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("mutant count = %d, want 5", len(entries))
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			checkout := newFixtureCheckout(t)
			copyFile(
				t,
				filepath.Join(mutantsRoot, entry.Name(), "ledger.go"),
				filepath.Join(checkout, "ledger", "ledger.go"),
			)
			copyFile(
				t,
				filepath.Join(graderRoot, "testdata", "hidden", "ledger_hidden_test.go"),
				filepath.Join(checkout, "ledger", "ledger_hidden_test.go"),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "./...")
			command.Dir = checkout
			command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
			output, runErr := command.CombinedOutput()
			if runErr == nil {
				t.Fatalf("hidden suite did not kill mutant %q:\n%s", entry.Name(), output)
			}
			if ctx.Err() != nil {
				t.Fatalf("mutant %q timed out: %v\n%s", entry.Name(), ctx.Err(), output)
			}
		})
	}
}

func TestGraderAcceptsReferenceAndReportsMutationEvidence(t *testing.T) {
	requireBenchmarkTools(t)
	checkout := newFixtureCheckout(t)
	commit := gitOutput(t, checkout, "rev-parse", "HEAD")
	graderRoot := graderRoot(t)
	referenceRoot := filepath.Join(graderRoot, "testdata", "reference")
	copyFile(t, filepath.Join(referenceRoot, "ledger.go"), filepath.Join(checkout, "ledger", "ledger.go"))
	copyFile(
		t,
		filepath.Join(referenceRoot, "ledger_candidate_test.go"),
		filepath.Join(checkout, "ledger", "ledger_candidate_test.go"),
	)
	beforeLedger := fileDigest(t, filepath.Join(checkout, "ledger", "ledger.go"))
	beforeTests := fileDigest(t, filepath.Join(checkout, "ledger", "ledger_candidate_test.go"))

	outputRoot := filepath.Join(t.TempDir(), "grader-output")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join(graderRoot, "grade.sh"), checkout, outputRoot, commit)
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("grade.sh error = %v\noutput:\n%s", err, output)
	}
	if ctx.Err() != nil {
		t.Fatalf("grade.sh timed out: %v", ctx.Err())
	}

	raw, err := os.ReadFile(filepath.Join(outputRoot, "grader.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact graderArtifact
	if err = json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("decode grader.json: %v\n%s", err, raw)
	}
	if artifact.Version != 2 || artifact.Fixture != "transfer-idempotency-v1" ||
		artifact.Score != 100 || !artifact.MandatoryPass {
		t.Fatalf("grader result = %#v", artifact)
	}
	if !artifact.Checks.Format || !artifact.Checks.Vet || !artifact.Checks.Test ||
		!artifact.Checks.Race || !artifact.Checks.Scope || !artifact.Checks.GitHead ||
		!artifact.Checks.TestsChanged {
		t.Fatalf("grader checks = %#v", artifact.Checks)
	}
	if artifact.Mutation.Killed != 5 || artifact.Mutation.Total != 5 ||
		artifact.Mutation.Points != 10 || len(artifact.Mutation.Mutants) != 5 {
		t.Fatalf("mutation result = %#v", artifact.Mutation)
	}
	for _, mutant := range artifact.Mutation.Mutants {
		if !mutant.Killed {
			t.Fatalf("mutant survived: %#v", mutant)
		}
	}
	sort.Strings(artifact.ChangedFiles)
	wantChanged := []string{"ledger/ledger.go", "ledger/ledger_candidate_test.go"}
	if !reflect.DeepEqual(artifact.ChangedFiles, wantChanged) {
		t.Fatalf("changed files = %#v, want %#v", artifact.ChangedFiles, wantChanged)
	}
	if _, statErr := os.Lstat(
		filepath.Join(checkout, "ledger", "ledger_hidden_test.go"),
	); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("hidden test remained in checkout: %v", statErr)
	}
	if after := fileDigest(t, filepath.Join(checkout, "ledger", "ledger.go")); after != beforeLedger {
		t.Fatal("grader changed candidate implementation")
	}
	if after := fileDigest(t, filepath.Join(checkout, "ledger", "ledger_candidate_test.go")); after != beforeTests {
		t.Fatal("grader changed candidate tests")
	}
}

func TestGraderRejectsLedgerSymlinkWithoutOutsideWrite(t *testing.T) {
	requireBenchmarkTools(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	checkout := newFixtureCheckout(t)
	ledgerPath := filepath.Join(checkout, "ledger")
	if err := os.RemoveAll(ledgerPath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ledgerPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	outputRoot := filepath.Join(t.TempDir(), "grader-output")
	command := exec.Command("bash", filepath.Join(graderRoot(t), "grade.sh"), checkout, outputRoot)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("grade.sh accepted symlinked ledger:\n%s", output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("grade.sh symlink exit = %v, want 2\n%s", err, output)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "ledger_hidden_test.go")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("grader wrote hidden test outside checkout: %v", statErr)
	}
	value, readErr := os.ReadFile(marker)
	if readErr != nil || string(value) != "unchanged" {
		t.Fatalf("outside marker changed: value=%q error=%v", value, readErr)
	}
}

func graderRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(
		graderRoot(t), "..", "..", "fixtures", "coding-agent-benchmark", "transfer-idempotency-v1",
	))
}

func newFixtureCheckout(t *testing.T) string {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), "checkout")
	copyTree(t, fixtureRoot(t), checkout)
	gitOutput(t, checkout, "init", "-b", "main")
	gitOutput(t, checkout, "add", "--all")
	gitOutput(
		t,
		checkout,
		"-c", "user.name=PicoClaw Benchmark", "-c", "user.email=benchmark.invalid@example.invalid",
		"commit", "-m", "fixture",
	)
	return checkout
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("fixture contains symlink: " + relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		copyFile(t, path, target)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	value, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, value, 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s error = %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func requireBenchmarkTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("benchmark grader requires POSIX process and permission semantics")
	}
	for _, name := range []string{"bash", "git", "go", "gofmt", "jq", "realpath", "sha256sum"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is unavailable", name)
		}
	}
}
