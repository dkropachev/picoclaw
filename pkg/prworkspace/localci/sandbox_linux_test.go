//go:build linux

package localci

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxSandboxUsesDisposableCredentialFreeOfflineWorkspace(t *testing.T) {
	candidate := t.TempDir()
	writeTestFile(t, candidate, "input.txt", "candidate\n")
	outside := filepath.Join(t.TempDir(), "outside-secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")

	sandbox := requireLinuxSandbox(t)
	step := testSandboxStep(fmt.Sprintf(`
test "$(cat input.txt)" = "candidate"
test ! -e generated.txt
test ! -e /workspace/.git
test ! -e %q
test -z "${AWS_SECRET_ACCESS_KEY+x}"
if (exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null; then
  exit 91
fi
printf generated > generated.txt
printf sandbox-ok
`, outside, port))
	result, err := sandbox.RunStep(context.Background(), candidate, step, DefaultLimits())
	if err != nil {
		t.Fatalf("RunStep() error = %v, result = %#v", err, result)
	}
	if result.Status != StatusPassed || result.ExitCode != 0 || !strings.Contains(result.Output, "sandbox-ok") {
		t.Fatalf("RunStep() result = %#v", result)
	}
	if _, err = os.Stat(filepath.Join(candidate, "generated.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox mutated candidate root; stat error = %v", err)
	}

	second := testSandboxStep("test ! -e generated.txt")
	result, err = sandbox.RunStep(context.Background(), candidate, second, DefaultLimits())
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("fresh RunStep() = (%#v, %v)", result, err)
	}
}

func TestRunnerRunPinnedUsesProductionLinuxSandbox(t *testing.T) {
	fixture := newPinnedRunnerFixture(t, "run-pinned-production-linux")
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Sandbox: requireLinuxSandbox(t), Store: store}

	result, err := runner.RunPinned(context.Background(), fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_production_linux",
		OwnerID:       "attempt_owner",
		Candidate:     fixture.validation,
	})
	if err != nil || result.Execution.Status != StatusPassed ||
		result.Attestation.Status != StatusPassed || result.Attestation.CacheHit {
		t.Fatalf("RunPinned(production Linux) = (%#v, %v)", result, err)
	}
}

func TestLinuxSandboxTimeoutAndOutputLimitKillStep(t *testing.T) {
	candidate := t.TempDir()
	sandbox := requireLinuxSandbox(t)
	timeoutStep := testSandboxStep("sleep 5")
	timeoutStep.TimeoutSeconds = 1
	started := time.Now()
	result, err := sandbox.RunStep(context.Background(), candidate, timeoutStep, Limits{
		StepTimeout:  time.Second,
		TotalTimeout: time.Second,
		OutputBytes:  1024,
	})
	if err != nil || result.Status != StatusTimedOut || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout RunStep() = (%#v, %v), elapsed %v", result, err, time.Since(started))
	}

	overflowStep := testSandboxStep("while :; do printf 0123456789; done")
	result, err = sandbox.RunStep(context.Background(), candidate, overflowStep, Limits{
		StepTimeout:  5 * time.Second,
		TotalTimeout: 5 * time.Second,
		OutputBytes:  1024,
	})
	if err != nil || result.Status != StatusOutputLimitExceeded || len(result.Output) != 1024 ||
		!result.OutputTruncated {
		t.Fatalf("overflow RunStep() = (%#v, %v)", result, err)
	}
}

func TestLinuxSandboxCannotHidePidsExhaustionBehindExitZero(t *testing.T) {
	candidate := t.TempDir()
	helperDirectory := t.TempDir()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableInfo, statErr := os.Stat(testExecutable)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if err = copySandboxFile(
		testExecutable,
		filepath.Join(helperDirectory, "pids-helper"),
		executableInfo,
	); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(SandboxConfig{
		TemporaryRoot: t.TempDir(),
		DependencyMounts: []DependencyMount{{
			Source: helperDirectory,
			Target: "/dependencies/bin",
			Digest: strings.Repeat("a", 64),
		}},
	})
	if errors.Is(err, ErrSandboxUnavailable) {
		t.Skipf("mandatory local CI backend unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	step := Step{
		ID:             "ci_pids_exhaustion",
		Name:           "pids exhaustion helper",
		Kind:           StepTest,
		Origin:         OriginExplicit,
		Source:         ".picoclaw/ci.yml",
		Argv:           []string{"pids-helper", "-test.run=^TestLocalCIPidsHelperProcess$"},
		Environment:    []EnvironmentVariable{{Name: "PICOCLAW_PIDS_HELPER", Value: "1"}},
		TimeoutSeconds: 10,
		Required:       true,
	}
	result, err := sandbox.RunStep(context.Background(), candidate, step, Limits{
		StepTimeout:  10 * time.Second,
		TotalTimeout: 10 * time.Second,
		OutputBytes:  64 << 10,
	})
	if !errors.Is(err, errLocalCIResourceLimit) || result.Status != StatusInfrastructureError {
		t.Fatalf("caught pids exhaustion = (%#v, %v), want non-green resource-limit evidence", result, err)
	}
}

func TestLocalCIPidsHelperProcess(t *testing.T) {
	if os.Getenv("PICOCLAW_PIDS_HELPER") != "1" {
		return
	}
	children := make([]*exec.Cmd, 0, 400)
	for range 400 {
		child := exec.Command("/bin/sleep", "30")
		if err := child.Start(); err != nil {
			break
		}
		children = append(children, child)
	}
	for _, child := range children {
		_ = child.Process.Kill()
	}
	for _, child := range children {
		_ = child.Wait()
	}
	_, _ = fmt.Fprintf(os.Stdout, "children=%d\n", len(children))
	os.Exit(0)
}

func TestLocalCIResourceLimitIncludesHandledMemoryMax(t *testing.T) {
	if !localCIResourceLimitReached(map[string]uint64{"max": 1}, nil) {
		t.Fatal("memory.events max was not classified as resource exhaustion")
	}
}

func TestClassifySandboxExitSeparatesInfrastructureFromCodeFailures(t *testing.T) {
	exit := &exec.ExitError{}
	// processExitCode on a synthetic ExitError is -1, which is sufficient for
	// exercising the output classifier without launching a process.
	tests := []struct {
		name   string
		output string
		want   Status
	}{
		{
			name:   "dependency network",
			output: "go: downloading example.test/module v1.0.0\ndial tcp: lookup proxy.golang.org: connection refused",
			want:   StatusInfrastructureError,
		},
		{
			name:   "process ceiling",
			output: "runtime: failed to create new OS thread\nresource temporarily unavailable",
			want:   StatusInfrastructureError,
		},
		{name: "ordinary assertion", output: "--- FAIL: TestGreeting\nwant hello, got goodbye", want: StatusFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, _ := classifySandboxExit(exit, []byte(test.output))
			if status != test.want {
				t.Fatalf("classifySandboxExit() = %q, want %q", status, test.want)
			}
		})
	}
}

func TestLinuxSandboxEnvironmentDigestBindsProcessGenerationAndPlan(t *testing.T) {
	first := requireLinuxSandbox(t)
	second := requireLinuxSandbox(t)
	plan := validTestPlan(t)
	firstDigest, err := first.EnvironmentDigest(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	firstReplay, err := first.EnvironmentDigest(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.EnvironmentDigest(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != firstReplay || firstDigest == secondDigest {
		t.Fatalf("environment digests = %q, %q, %q", firstDigest, firstReplay, secondDigest)
	}
}

func TestLinuxSandboxEnvironmentDigestRejectsMissingRequiredExecutable(t *testing.T) {
	sandbox := requireLinuxSandbox(t)
	plan := validTestPlan(t)
	plan.Steps[0].Argv = []string{"picoclaw-command-that-does-not-exist"}
	plan.Steps[0].Script = ""
	plan.Steps[0].Shell = ""
	plan, err := normalizePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sandbox.EnvironmentDigest(context.Background(), plan); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("EnvironmentDigest() error = %v, want environment unavailable", err)
	}
}

func TestNewSandboxRejectsNestedDependencyTarget(t *testing.T) {
	_, err := NewSandbox(SandboxConfig{
		TemporaryRoot: t.TempDir(),
		DependencyMounts: []DependencyMount{{
			Source: t.TempDir(),
			Target: "/dependencies/nested/tools",
			Digest: strings.Repeat("a", 64),
		}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewSandbox() error = %v, want invalid target", err)
	}
}

func TestLinuxSandboxRejectsGitControlPath(t *testing.T) {
	candidate := t.TempDir()
	writeTestFile(t, candidate, ".git/config", "unsafe")
	sandbox := requireLinuxSandbox(t)
	result, err := sandbox.RunStep(context.Background(), candidate, testSandboxStep("true"), DefaultLimits())
	if err == nil || result.Status != StatusInfrastructureError {
		t.Fatalf("RunStep(.git) = (%#v, %v), want rejection", result, err)
	}
}

func testSandboxStep(script string) Step {
	return Step{
		ID:             "ci_sandbox",
		Name:           "sandbox",
		Kind:           StepTest,
		Origin:         OriginExplicit,
		Source:         ".picoclaw/ci.yml",
		Script:         script,
		Shell:          "bash",
		TimeoutSeconds: 30,
		Required:       true,
	}
}

func requireLinuxSandbox(t *testing.T) Sandbox {
	t.Helper()
	sandbox, err := NewSandbox(SandboxConfig{TemporaryRoot: t.TempDir()})
	if errors.Is(err, ErrSandboxUnavailable) {
		t.Skipf("mandatory local CI backend unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("NewSandbox() error = %v", err)
	}
	return sandbox
}
