package isolation

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	executionPolicyHelperEnvironment = "PICOCLAW_EXECUTION_POLICY_HELPER"
	executionPolicyReadyEnvironment  = "PICOCLAW_EXECUTION_POLICY_READY"
	executionPolicyHelperArgument    = "p013-execution-policy-helper"
)

func TestExecutionPolicyHelperProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != executionPolicyHelperArgument {
		return
	}
	switch os.Getenv(executionPolicyHelperEnvironment) {
	case "run":
		return
	case "exit-7":
		os.Exit(7)
	case "block":
		if readyPath := os.Getenv(executionPolicyReadyEnvironment); readyPath != "" {
			if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
				t.Fatalf("write execution-policy helper readiness: %v", err)
			}
		}
		time.Sleep(30 * time.Second)
		return
	case "legacy-default":
		if err := Preflight(); err != nil {
			t.Fatalf("fresh-process default Preflight() error = %v", err)
		}
		prepared := executionPolicyHelperCommand()
		wantPath := prepared.Path
		if err := PrepareCommand(prepared); err != nil || prepared.Path != wantPath {
			t.Fatalf(
				"fresh-process default PrepareCommand() = path %q, error %v; want %q",
				prepared.Path,
				err,
				wantPath,
			)
		}
		started := executionPolicyHelperCommand()
		if err := Start(started); err != nil {
			t.Fatalf("fresh-process default Start() error = %v", err)
		}
		if err := started.Wait(); err != nil {
			t.Fatalf("fresh-process default Start() wait error = %v", err)
		}
		if err := Run(executionPolicyHelperCommand()); err != nil {
			t.Fatalf("fresh-process default Run() error = %v", err)
		}
	default:
		t.Fatalf("unknown execution-policy helper mode %q", os.Getenv(executionPolicyHelperEnvironment))
	}
}

func TestExecutionPolicyDetachesSourceAndLaunchProjections(t *testing.T) {
	paths := make([]config.ExposePath, 1, 4)
	paths[0] = config.ExposePath{
		Source: "/source-a",
		Target: "/target-a",
		Mode:   "ro",
	}
	source := config.IsolationConfig{
		Enabled:     true,
		ExposePaths: paths,
	}
	policy := NewExecutionPolicy(source)

	// Mutate the scalar, existing element, length, and spare capacity retained by
	// the caller. None of those aliases may reach the policy snapshot.
	source.Enabled = false
	paths[0] = config.ExposePath{Source: "/source-mutated", Target: "/target-mutated", Mode: "rw"}
	source.ExposePaths = append(source.ExposePaths,
		config.ExposePath{Source: "/source-spare", Target: "/target-spare", Mode: "rw"})

	want := config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/source-a",
			Target: "/target-a",
			Mode:   "ro",
		}},
	}
	first, ok := policy.detachedIsolation()
	if !ok || !reflect.DeepEqual(first, want) {
		t.Fatalf("detached policy = %#v, %v; want %#v, true", first, ok, want)
	}

	// A launch-facing clone is also caller-owned. Mutating it cannot alter the
	// stored policy or another projection made later.
	first.Enabled = false
	first.ExposePaths[0].Source = "/projection-mutated"
	first.ExposePaths = append(first.ExposePaths,
		config.ExposePath{Source: "/projection-extra", Target: "/projection-extra", Mode: "ro"})
	second, ok := policy.detachedIsolation()
	if !ok || !reflect.DeepEqual(second, want) {
		t.Fatalf("policy followed detached projection mutation: %#v, %v", second, ok)
	}

	copied := policy
	third, ok := copied.detachedIsolation()
	if !ok || !reflect.DeepEqual(third, want) {
		t.Fatalf("copied policy = %#v, %v; want %#v, true", third, ok, want)
	}
}

func TestExecutionPolicyPreservesNilAndAllocatedEmptyExposePaths(t *testing.T) {
	nilPolicy := NewExecutionPolicy(config.IsolationConfig{ExposePaths: nil})
	nilConfig, ok := nilPolicy.detachedIsolation()
	if !ok || nilConfig.ExposePaths != nil {
		t.Fatalf("nil expose paths = %#v, %v; want nil, true", nilConfig.ExposePaths, ok)
	}

	emptySource := make([]config.ExposePath, 0)
	emptyPolicy := NewExecutionPolicy(config.IsolationConfig{ExposePaths: emptySource})
	emptyConfig, ok := emptyPolicy.detachedIsolation()
	if !ok || emptyConfig.ExposePaths == nil || len(emptyConfig.ExposePaths) != 0 {
		t.Fatalf("allocated empty expose paths = %#v, %v; want non-nil empty", emptyConfig.ExposePaths, ok)
	}

	Configure(&config.Config{Isolation: config.IsolationConfig{ExposePaths: nil}})
	t.Cleanup(func() { Configure(nil) })
	if got := CurrentConfig().ExposePaths; got != nil {
		t.Fatalf("legacy nil expose paths = %#v, want nil", got)
	}
	Configure(&config.Config{Isolation: config.IsolationConfig{
		ExposePaths: make([]config.ExposePath, 0),
	}})
	if got := CurrentConfig().ExposePaths; got == nil || len(got) != 0 {
		t.Fatalf("legacy allocated empty expose paths = %#v, want non-nil empty", got)
	}
}

func TestExecutionPolicyZeroAndNilCommandFailClosed(t *testing.T) {
	var zero ExecutionPolicy
	for name, run := range map[string]func() error{
		"start": func() error { return zero.Start(exec.Command("unused")) },
		"run":   func() error { return zero.Run(exec.Command("unused")) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrExecutionPolicyUnavailable) {
				t.Fatalf("zero policy error = %v, want %v", err, ErrExecutionPolicyUnavailable)
			}
		})
	}

	_, projectionErr := projectionForPolicy(zero, launchOperations{goos: "linux"})
	if !errors.Is(projectionErr, ErrExecutionPolicyUnavailable) {
		t.Fatalf("zero projection error = %v, want %v", projectionErr, ErrExecutionPolicyUnavailable)
	}

	disabled := NewExecutionPolicy(config.IsolationConfig{})
	for name, run := range map[string]func() error{
		"start": func() error { return disabled.Start(nil) },
		"run":   func() error { return disabled.Run(nil) },
	} {
		t.Run("nil-command-"+name, func(t *testing.T) {
			if err := run(); err == nil || errors.Is(err, ErrExecutionPolicyUnavailable) {
				t.Fatalf("nil command error = %v, want non-policy command error", err)
			}
		})
	}

	var sideEffects atomic.Int32
	operations := completeExecutionPolicyTestOperations("linux", t.TempDir())
	operations.resolveRoot = func() (string, error) {
		sideEffects.Add(1)
		return t.TempDir(), nil
	}
	operations.prepareRoot = func(string) error {
		sideEffects.Add(1)
		return nil
	}
	operations.apply = func(*exec.Cmd, launchProjection) error {
		sideEffects.Add(1)
		return nil
	}
	_, prepareErr := prepareCommandForPolicy(
		NewExecutionPolicy(config.IsolationConfig{Enabled: true}),
		nil,
		operations,
	)
	if prepareErr == nil || sideEffects.Load() != 0 {
		t.Fatalf("enabled nil command error=%v, side effects=%d", prepareErr, sideEffects.Load())
	}
}

func TestExecutionPolicyDisabledAllowsDormantInvalidExposure(t *testing.T) {
	policy := NewExecutionPolicy(config.IsolationConfig{
		ExposePaths: []config.ExposePath{
			{Source: "relative", Target: "also-relative", Mode: "bad"},
			{Source: "duplicate", Target: "also-relative", Mode: "bad"},
		},
	})
	operations := completeExecutionPolicyTestOperations("netbsd", "")
	var applyCalls, startCalls atomic.Int32
	operations.apply = func(*exec.Cmd, launchProjection) error {
		applyCalls.Add(1)
		return nil
	}
	operations.start = func(*exec.Cmd) error {
		startCalls.Add(1)
		return nil
	}
	if err := startExecutionPolicy(policy, exec.Command("disabled-dormant"), operations); err != nil {
		t.Fatalf("disabled dormant policy error = %v", err)
	}
	if applyCalls.Load() != 0 || startCalls.Load() != 1 {
		t.Fatalf("disabled dormant calls apply=%d start=%d", applyCalls.Load(), startCalls.Load())
	}
}

func TestExecutionPolicyDisabledPreservesCommandBeforeExactStart(t *testing.T) {
	stdin := strings.NewReader("input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	sysProcAttr := &syscall.SysProcAttr{}
	cmd := exec.Command("disabled-command", "argument")
	cmd.Dir = "relative-working-dir"
	cmd.Env = []string{"B=2", "A=1"}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = sysProcAttr
	wantPath := cmd.Path
	wantArgs := append([]string(nil), cmd.Args...)
	wantEnv := append([]string(nil), cmd.Env...)
	var applyCalls, startCalls atomic.Int32
	operations := completeExecutionPolicyTestOperations("netbsd", "")
	operations.apply = func(*exec.Cmd, launchProjection) error {
		applyCalls.Add(1)
		return nil
	}
	operations.start = func(got *exec.Cmd) error {
		startCalls.Add(1)
		if got.Path != wantPath || !reflect.DeepEqual(got.Args, wantArgs) ||
			got.Dir != "relative-working-dir" || !reflect.DeepEqual(got.Env, wantEnv) ||
			got.Stdin != stdin || got.Stdout != stdout || got.Stderr != stderr ||
			got.SysProcAttr != sysProcAttr {
			return fmt.Errorf("disabled command mutated before start: %#v", got)
		}
		return nil
	}
	if err := startExecutionPolicy(
		NewExecutionPolicy(config.IsolationConfig{}),
		cmd,
		operations,
	); err != nil {
		t.Fatalf("disabled exact start error = %v", err)
	}
	if applyCalls.Load() != 0 || startCalls.Load() != 1 {
		t.Fatalf("disabled exact start calls apply=%d start=%d", applyCalls.Load(), startCalls.Load())
	}
}

func TestConfigureDeepCopiesAndRestoresDefaultIsolation(t *testing.T) {
	paths := make([]config.ExposePath, 1, 3)
	paths[0] = config.ExposePath{Source: "/legacy-source", Target: "/legacy-target", Mode: "ro"}
	cfg := &config.Config{Isolation: config.IsolationConfig{
		Enabled:     true,
		ExposePaths: paths,
	}}
	Configure(cfg)
	t.Cleanup(func() { Configure(nil) })

	paths[0] = config.ExposePath{Source: "/mutated-source", Target: "/mutated-target", Mode: "rw"}
	cfg.Isolation.Enabled = false
	cfg.Isolation.ExposePaths = append(cfg.Isolation.ExposePaths,
		config.ExposePath{Source: "/spare-source", Target: "/spare-target", Mode: "rw"})

	want := config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/legacy-source",
			Target: "/legacy-target",
			Mode:   "ro",
		}},
	}
	first := CurrentConfig()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("legacy policy followed Configure input mutation: %#v", first)
	}
	first.Enabled = false
	first.ExposePaths[0].Source = "/returned-mutation"
	if second := CurrentConfig(); !reflect.DeepEqual(second, want) {
		t.Fatalf("legacy policy followed CurrentConfig output mutation: %#v", second)
	}

	Configure(nil)
	defaults := config.DefaultConfig().Isolation
	if got := CurrentConfig(); !reflect.DeepEqual(got, defaults) || got.Enabled {
		t.Fatalf("Configure(nil) = %#v, want disabled defaults %#v", got, defaults)
	}
}

func TestCurrentConfigFailsClosedForImpossibleZeroLegacyStore(t *testing.T) {
	isolationMu.Lock()
	previous := legacyPolicy
	legacyPolicy = ExecutionPolicy{}
	isolationMu.Unlock()
	t.Cleanup(func() {
		isolationMu.Lock()
		legacyPolicy = previous
		isolationMu.Unlock()
	})

	if got := CurrentConfig(); !reflect.DeepEqual(got, config.IsolationConfig{}) {
		t.Fatalf("CurrentConfig() with invalid internal store = %#v, want disabled zero config", got)
	}
	if err := prepareLegacyPolicy(
		exec.Command("invalid-internal-policy"),
		completeExecutionPolicyTestOperations("linux", ""),
	); !errors.Is(err, ErrExecutionPolicyUnavailable) {
		t.Fatalf("prepareLegacyPolicy() invalid store error = %v", err)
	}
}

func TestLegacyIsolationDefaultWrappersAndExplicitDisabledPolicy(t *testing.T) {
	Configure(nil)
	t.Cleanup(func() { Configure(nil) })
	if err := Preflight(); err != nil {
		t.Fatalf("default Preflight() error = %v", err)
	}

	prepared := executionPolicyHelperCommand()
	wantPath := prepared.Path
	wantArgs := append([]string(nil), prepared.Args...)
	wantEnv := append([]string(nil), prepared.Env...)
	if err := PrepareCommand(prepared); err != nil {
		t.Fatalf("default PrepareCommand() error = %v", err)
	}
	if prepared.Path != wantPath || !reflect.DeepEqual(prepared.Args, wantArgs) ||
		!reflect.DeepEqual(prepared.Env, wantEnv) {
		t.Fatalf(
			"disabled PrepareCommand mutated command: path=%q args=%#v env=%#v",
			prepared.Path,
			prepared.Args,
			prepared.Env,
		)
	}

	started := executionPolicyHelperCommand()
	if err := Start(started); err != nil {
		t.Fatalf("default Start() error = %v", err)
	}
	if err := started.Wait(); err != nil {
		t.Fatalf("wait after default Start() = %v", err)
	}
	if err := Run(executionPolicyHelperCommand()); err != nil {
		t.Fatalf("default Run() error = %v", err)
	}
	var legacyExitErr *exec.ExitError
	legacyRunErr := Run(executionPolicyHelperCommandWithMode("exit-7"))
	if !errors.As(legacyRunErr, &legacyExitErr) || legacyExitErr.ExitCode() != 7 {
		t.Fatalf("default Run() nonzero error = %v, exit=%v", legacyRunErr, legacyExitErr)
	}

	disabled := NewExecutionPolicy(config.IsolationConfig{})
	explicitStart := executionPolicyHelperCommand()
	if err := disabled.Start(explicitStart); err != nil {
		t.Fatalf("explicit disabled Start() error = %v", err)
	}
	if err := explicitStart.Wait(); err != nil {
		t.Fatalf("wait after explicit disabled Start() = %v", err)
	}
	if err := disabled.Run(executionPolicyHelperCommand()); err != nil {
		t.Fatalf("explicit disabled Run() error = %v", err)
	}
	var exitErr *exec.ExitError
	err := disabled.Run(executionPolicyHelperCommandWithMode("exit-7"))
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("explicit disabled Run() nonzero error = %v, exit=%v", err, exitErr)
	}
}

func TestLegacyIsolationFreshProcessUsesConstructedDefault(t *testing.T) {
	if err := executionPolicyHelperCommandWithMode("legacy-default").Run(); err != nil {
		t.Fatalf("fresh-process legacy default verification error = %v", err)
	}
}

func TestExecutionPolicyHelperRejectsUnknownMode(t *testing.T) {
	if err := executionPolicyHelperCommandWithMode("unknown").Run(); err == nil {
		t.Fatal("execution-policy helper accepted unknown mode")
	}
}

func TestLegacyIsolationSelectedPolicyRemainsCoherent(t *testing.T) {
	root := t.TempDir()
	a := &config.Config{Isolation: config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/legacy-a-source",
			Target: "/legacy-a-target",
			Mode:   "ro",
		}},
	}}
	b := &config.Config{Isolation: config.IsolationConfig{
		Enabled: false,
		ExposePaths: []config.ExposePath{{
			Source: "/legacy-b-source",
			Target: "/legacy-b-target",
			Mode:   "rw",
		}},
	}}
	Configure(a)
	t.Cleanup(func() { Configure(nil) })

	var applied, posted launchProjection
	var resolveCalls atomic.Int32
	ops := completeExecutionPolicyTestOperations("linux", root)
	ops.resolveRoot = func() (string, error) {
		resolveCalls.Add(1)
		return root, nil
	}
	ops.apply = func(_ *exec.Cmd, launch launchProjection) error {
		applied = cloneExecutionPolicyTestLaunch(launch)
		Configure(b)
		return nil
	}
	ops.postStart = func(_ *exec.Cmd, launch launchProjection) error {
		posted = cloneExecutionPolicyTestLaunch(launch)
		return nil
	}
	if err := startLegacyPolicy(exec.Command("legacy-snapshot"), ops); err != nil {
		t.Fatalf("selected legacy launch error = %v", err)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("root resolution calls = %d, want 1", resolveCalls.Load())
	}
	if !reflect.DeepEqual(applied, posted) {
		t.Fatalf("legacy launch mixed projections:\napply=%#v\npost=%#v", applied, posted)
	}
	if !applied.isolation.Enabled || applied.root != root ||
		len(applied.isolation.ExposePaths) != 1 ||
		applied.isolation.ExposePaths[0].Source != "/legacy-a-source" {
		t.Fatalf("selected launch = %#v, want exact legacy A", applied)
	}
	if got := CurrentConfig(); !reflect.DeepEqual(got, b.Isolation) {
		t.Fatalf("current legacy config = %#v, want B %#v", got, b.Isolation)
	}
}

func TestLegacyAndExplicitIsolationProjectionParity(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Isolation: config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/parity-source",
			Target: "/parity-target",
			Mode:   "ro",
		}},
	}}
	Configure(cfg)
	t.Cleanup(func() { Configure(nil) })

	capture := func(destination *launchProjection) launchOperations {
		operations := completeExecutionPolicyTestOperations("linux", root)
		operations.apply = func(_ *exec.Cmd, launch launchProjection) error {
			*destination = cloneExecutionPolicyTestLaunch(launch)
			return nil
		}
		return operations
	}
	var explicit, legacy launchProjection
	explicitCmd := exec.Command("explicit-parity")
	explicitCmd.Env = []string{"PARITY=exact"}
	if err := startExecutionPolicy(
		NewExecutionPolicy(cfg.Isolation),
		explicitCmd,
		capture(&explicit),
	); err != nil {
		t.Fatalf("explicit parity launch error = %v", err)
	}
	legacyCmd := exec.Command("legacy-parity")
	legacyCmd.Env = []string{"PARITY=exact"}
	if err := startLegacyPolicy(legacyCmd, capture(&legacy)); err != nil {
		t.Fatalf("legacy parity launch error = %v", err)
	}
	if !reflect.DeepEqual(explicit, legacy) {
		t.Fatalf("explicit/legacy projections differ:\nexplicit=%#v\nlegacy=%#v", explicit, legacy)
	}
}

func TestLegacyPrepareCommandFailsClosedForEnabledWindows(t *testing.T) {
	Configure(&config.Config{Isolation: config.IsolationConfig{Enabled: true}})
	t.Cleanup(func() { Configure(nil) })
	var rootCalls, applyCalls atomic.Int32
	operations := completeExecutionPolicyTestOperations("windows", t.TempDir())
	operations.resolveRoot = func() (string, error) {
		rootCalls.Add(1)
		return t.TempDir(), nil
	}
	operations.apply = func(*exec.Cmd, launchProjection) error {
		applyCalls.Add(1)
		return nil
	}
	err := prepareLegacyPolicy(exec.Command("windows-prepare"), operations)
	if err == nil || !strings.Contains(err.Error(), "cannot complete Windows isolation") {
		t.Fatalf("enabled Windows legacy prepare error = %v", err)
	}
	if rootCalls.Load() != 0 || applyCalls.Load() != 0 {
		t.Fatalf("enabled Windows legacy prepare effects root=%d apply=%d",
			rootCalls.Load(), applyCalls.Load())
	}
}

func TestConfigureConcurrentSnapshotsNeverHybrid(t *testing.T) {
	a := &config.Config{Isolation: config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/configure-a-source",
			Target: "/configure-a-target",
			Mode:   "ro",
		}},
	}}
	b := &config.Config{Isolation: config.IsolationConfig{
		Enabled: false,
		ExposePaths: []config.ExposePath{{
			Source: "/configure-b-source",
			Target: "/configure-b-target",
			Mode:   "rw",
		}},
	}}
	Configure(a)
	t.Cleanup(func() { Configure(nil) })

	const iterations = 500
	start := make(chan struct{})
	errorsSeen := make(chan error, 8)
	var ready sync.WaitGroup
	var workers sync.WaitGroup
	ready.Add(6)
	for writer := range 2 {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			ready.Done()
			<-start
			for index := range iterations {
				runtime.Gosched()
				if (index+offset)%2 == 0 {
					Configure(a)
				} else {
					Configure(b)
				}
			}
		}(writer)
	}
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ready.Done()
			<-start
			for range iterations {
				runtime.Gosched()
				got := CurrentConfig()
				if !reflect.DeepEqual(got, a.Isolation) && !reflect.DeepEqual(got, b.Isolation) {
					errorsSeen <- fmt.Errorf("hybrid legacy isolation snapshot: %#v", got)
					return
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestExecutionPolicyRootAndPlatformProjectionValidation(t *testing.T) {
	t.Run("relative process home", func(t *testing.T) {
		t.Setenv(config.EnvHome, "relative-p013-home")
		if root, err := ResolveInstanceRoot(); err == nil || root != "" {
			t.Fatalf("ResolveInstanceRoot() = %q, %v; want empty error", root, err)
		}
	})

	t.Run("disabled unsupported skips root", func(t *testing.T) {
		var called atomic.Bool
		operations := completeExecutionPolicyTestOperations("darwin", "")
		operations.resolveRoot = func() (string, error) {
			called.Store(true)
			return "", errors.New("unexpected root resolution")
		}
		operations.prepareRoot = func(string) error {
			called.Store(true)
			return errors.New("unexpected root preparation")
		}
		launch, err := buildLaunchProjection(config.IsolationConfig{}, operations)
		if err != nil || launch.isolation.Enabled || launch.goos != "darwin" || called.Load() {
			t.Fatalf("disabled Darwin projection = %#v, %v, called=%v", launch, err, called.Load())
		}
	})

	t.Run("enabled unsupported fails before root", func(t *testing.T) {
		for _, goos := range []string{"darwin", "freebsd", "netbsd"} {
			t.Run(goos, func(t *testing.T) {
				var called atomic.Bool
				operations := completeExecutionPolicyTestOperations(goos, "")
				operations.resolveRoot = func() (string, error) {
					called.Store(true)
					return "", nil
				}
				operations.prepareRoot = func(string) error {
					called.Store(true)
					return nil
				}
				_, err := buildLaunchProjection(config.IsolationConfig{Enabled: true}, operations)
				if err == nil || !strings.Contains(err.Error(), "not supported on "+goos) || called.Load() {
					t.Fatalf("enabled %s error = %v, called=%v", goos, err, called.Load())
				}
			})
		}
	})

	t.Run("relative root fails before preparation", func(t *testing.T) {
		var prepareCalls atomic.Int32
		operations := completeExecutionPolicyTestOperations("linux", "relative-root")
		operations.prepareRoot = func(string) error {
			prepareCalls.Add(1)
			return nil
		}
		_, err := buildLaunchProjection(config.IsolationConfig{Enabled: true}, operations)
		if err == nil || !strings.Contains(err.Error(), "absolute") || prepareCalls.Load() != 0 {
			t.Fatalf("relative root error = %v, prepare calls=%d", err, prepareCalls.Load())
		}
	})

	t.Run("root resolution error fails before preparation", func(t *testing.T) {
		resolveErr := errors.New("injected root resolution failure")
		var prepareCalls atomic.Int32
		operations := completeExecutionPolicyTestOperations("linux", "")
		operations.resolveRoot = func() (string, error) { return "", resolveErr }
		operations.prepareRoot = func(string) error {
			prepareCalls.Add(1)
			return nil
		}
		_, err := buildLaunchProjection(config.IsolationConfig{Enabled: true}, operations)
		if !errors.Is(err, resolveErr) || prepareCalls.Load() != 0 {
			t.Fatalf("root resolution error = %v, prepare calls=%d", err, prepareCalls.Load())
		}
	})

	t.Run("invalid exposure fails before preparation", func(t *testing.T) {
		var prepareCalls atomic.Int32
		operations := completeExecutionPolicyTestOperations("linux", t.TempDir())
		operations.prepareRoot = func(string) error {
			prepareCalls.Add(1)
			return nil
		}
		_, err := buildLaunchProjection(config.IsolationConfig{
			Enabled: true,
			ExposePaths: []config.ExposePath{{
				Source: "relative", Target: "/target", Mode: "ro",
			}},
		}, operations)
		if err == nil || prepareCalls.Load() != 0 {
			t.Fatalf("invalid exposure error = %v, prepare calls=%d", err, prepareCalls.Load())
		}
	})

	t.Run("root preparation error is retained", func(t *testing.T) {
		prepareErr := errors.New("injected root preparation failure")
		operations := completeExecutionPolicyTestOperations("linux", t.TempDir())
		operations.prepareRoot = func(string) error { return prepareErr }
		_, err := buildLaunchProjection(config.IsolationConfig{Enabled: true}, operations)
		if !errors.Is(err, prepareErr) {
			t.Fatalf("root preparation error = %v, want %v", err, prepareErr)
		}
	})

	t.Run("Linux retains exact base projection", func(t *testing.T) {
		root := t.TempDir()
		cfg := config.IsolationConfig{
			Enabled: true,
			ExposePaths: []config.ExposePath{{
				Source: "/p013-linux-source",
				Target: "/p013-linux-target",
				Mode:   "ro",
			}},
		}
		var prepared string
		operations := completeExecutionPolicyTestOperations("linux", root)
		operations.prepareRoot = func(got string) error {
			prepared = got
			return nil
		}
		launch, err := buildLaunchProjection(cfg, operations)
		if err != nil {
			t.Fatalf("Linux projection error = %v", err)
		}
		if launch.root != root || launch.goos != "linux" || prepared != root ||
			len(launch.linuxBaseMounts) == 0 ||
			launch.linuxBaseMounts[0] != (MountRule{Source: root, Target: root, Mode: "rw"}) ||
			!executionPolicyTestHasMount(launch.linuxBaseMounts,
				MountRule{Source: "/p013-linux-source", Target: "/p013-linux-target", Mode: "ro"}) {
			t.Fatalf("Linux projection = %#v, prepared=%q", launch, prepared)
		}
	})

	t.Run("Windows access DTO and expose rejection", func(t *testing.T) {
		root := t.TempDir()
		var prepareCalls atomic.Int32
		operations := completeExecutionPolicyTestOperations("windows", root)
		operations.prepareRoot = func(got string) error {
			if got != root {
				return fmt.Errorf("prepared root %q, want %q", got, root)
			}
			prepareCalls.Add(1)
			return nil
		}
		launch, err := buildLaunchProjection(config.IsolationConfig{Enabled: true}, operations)
		if err != nil {
			t.Fatalf("Windows projection error = %v", err)
		}
		wantAccess := []AccessRule{{Path: root, Mode: "rw"}}
		if !reflect.DeepEqual(launch.windowsAccess, wantAccess) || prepareCalls.Load() != 1 {
			t.Fatalf("Windows access = %#v, prepare calls=%d", launch.windowsAccess, prepareCalls.Load())
		}

		prepareCalls.Store(0)
		_, err = buildLaunchProjection(config.IsolationConfig{
			Enabled: true,
			ExposePaths: []config.ExposePath{{
				Source: "/p013-windows-source",
				Target: "/p013-windows-target",
				Mode:   "ro",
			}},
		}, operations)
		if err == nil || !strings.Contains(err.Error(), "does not yet support expose_paths") ||
			prepareCalls.Load() != 0 {
			t.Fatalf("Windows expose error = %v, prepare calls=%d", err, prepareCalls.Load())
		}
	})
}

func TestApplyUserEnvUsesDetachedDeterministicProjection(t *testing.T) {
	base := []string{"ZED=last", "HOME=ambient", "ALPHA=first"}
	cmd := exec.Command("unused")
	cmd.Env = append([]string(nil), base...)
	root := t.TempDir()
	want := projectUserEnvironment(base, ResolveUserEnv(root), runtime.GOOS)

	ApplyUserEnv(cmd, root)
	if !reflect.DeepEqual(cmd.Env, want) || !sort.StringsAreSorted(cmd.Env) {
		t.Fatalf("ApplyUserEnv() = %#v, want sorted %#v", cmd.Env, want)
	}
	userBase := filepath.Join(root, "runtime-user-env")
	home := filepath.Join(userBase, "home")
	tmp := filepath.Join(userBase, "tmp")
	exactValues := []string{
		"HOME=" + home,
	}
	if runtime.GOOS == "windows" {
		exactValues = append(exactValues,
			"USERPROFILE="+home,
			"TEMP="+tmp,
			"TMP="+tmp,
			"APPDATA="+filepath.Join(userBase, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(userBase, "AppData", "Local"),
		)
	} else {
		exactValues = append(exactValues,
			"TMPDIR="+tmp,
			"XDG_CONFIG_HOME="+filepath.Join(userBase, "config"),
			"XDG_CACHE_HOME="+filepath.Join(userBase, "cache"),
			"XDG_STATE_HOME="+filepath.Join(userBase, "state"),
		)
	}
	for _, exact := range exactValues {
		if !executionPolicyTestEnvironmentContains(cmd.Env, exact) {
			t.Fatalf("ApplyUserEnv() missing independently expected %q in %#v", exact, cmd.Env)
		}
	}
	cmd.Env[0] = "MUTATED=1"
	if again := projectUserEnvironment(base, ResolveUserEnv(root), runtime.GOOS); !reflect.DeepEqual(again, want) {
		t.Fatalf("environment projection followed command mutation: %#v", again)
	}
	ApplyUserEnv(nil, root)
}

func TestExecutionPolicySequentialLaunchesResolveRootOnceEach(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	var resolveCalls atomic.Int32
	operations := completeExecutionPolicyTestOperations("linux", roots[0])
	operations.resolveRoot = func() (string, error) {
		index := int(resolveCalls.Add(1)) - 1
		if index >= len(roots) {
			return "", errors.New("root resolver called too many times")
		}
		return roots[index], nil
	}
	var observed []string
	operations.apply = func(_ *exec.Cmd, launch launchProjection) error {
		observed = append(observed, launch.root)
		return nil
	}
	policy := NewExecutionPolicy(config.IsolationConfig{Enabled: true})
	for range roots {
		if err := startExecutionPolicy(policy, exec.Command("unused"), operations); err != nil {
			t.Fatalf("sequential start error = %v", err)
		}
	}
	if resolveCalls.Load() != int32(len(roots)) || !reflect.DeepEqual(observed, roots) {
		t.Fatalf("root resolutions=%d observed=%#v, want %d %#v",
			resolveCalls.Load(), observed, len(roots), roots)
	}
}

func TestExecutionPolicyWindowsEnvironmentCaseFoldAndOrdering(t *testing.T) {
	base := []string{
		"Path=first-path",
		"HOME=ambient-home",
		"path=last-path",
		"Temp=ambient-temp",
		"Other=first-other",
		"OTHER=last-other",
		"EQUAL=a=b",
		"malformed",
	}
	wantBase := append([]string(nil), base...)
	userEnv := UserEnv{
		Home:         `C:\Pico\home`,
		Tmp:          `C:\Pico\tmp`,
		AppData:      `C:\Pico\AppData\Roaming`,
		LocalAppData: `C:\Pico\AppData\Local`,
	}
	want := []string{
		`APPDATA=C:\Pico\AppData\Roaming`,
		"EQUAL=a=b",
		`HOME=C:\Pico\home`,
		`LOCALAPPDATA=C:\Pico\AppData\Local`,
		"OTHER=last-other",
		"PATH=last-path",
		`TEMP=C:\Pico\tmp`,
		`TMP=C:\Pico\tmp`,
		`USERPROFILE=C:\Pico\home`,
	}
	got := projectUserEnvironment(base, userEnv, "windows")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Windows environment:\n got %#v\nwant %#v", got, want)
	}
	if !reflect.DeepEqual(base, wantBase) {
		t.Fatalf("environment projection mutated source: %#v", base)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Windows environment is not sorted: %#v", got)
	}
	seen := make(map[string]struct{}, len(got))
	for _, item := range got {
		key, _, _ := strings.Cut(item, "=")
		folded := strings.ToUpper(key)
		if _, duplicate := seen[folded]; duplicate {
			t.Fatalf("duplicate logical Windows key %q in %#v", key, got)
		}
		seen[folded] = struct{}{}
	}

	got[0] = "MUTATED=1"
	if again := projectUserEnvironment(base, userEnv, "windows"); !reflect.DeepEqual(again, want) {
		t.Fatalf("later Windows projection followed result mutation: %#v", again)
	}
}

func TestExecutionPolicyLaunchSnapshotAndLifecycle(t *testing.T) {
	t.Run("projection failure leaves command and platform untouched", func(t *testing.T) {
		var applyCalls atomic.Int32
		operations := completeExecutionPolicyTestOperations("linux", "relative-root")
		operations.apply = func(*exec.Cmd, launchProjection) error {
			applyCalls.Add(1)
			return nil
		}
		cmd := exec.Command("unchanged-command", "argument")
		wantPath := cmd.Path
		wantArgs := append([]string(nil), cmd.Args...)
		_, err := prepareCommandForPolicy(
			NewExecutionPolicy(config.IsolationConfig{Enabled: true}),
			cmd,
			operations,
		)
		if err == nil || applyCalls.Load() != 0 || cmd.Path != wantPath ||
			!reflect.DeepEqual(cmd.Args, wantArgs) || cmd.Env != nil {
			t.Fatalf("projection failure error=%v apply=%d command=%#v",
				err, applyCalls.Load(), cmd)
		}
	})

	t.Run("one projection crosses global change and command mutation", func(t *testing.T) {
		root := t.TempDir()
		cfg := config.IsolationConfig{
			Enabled: true,
			ExposePaths: []config.ExposePath{{
				Source: "/snapshot-source",
				Target: "/snapshot-target",
				Mode:   "ro",
			}},
		}
		policy := NewExecutionPolicy(cfg)
		cfg.Enabled = false
		cfg.ExposePaths[0].Source = "/source-mutated-after-construction"

		var resolveCalls, prepareCalls, startCalls, postCalls atomic.Int32
		var applied, posted launchProjection
		operations := completeExecutionPolicyTestOperations("linux", root)
		operations.resolveRoot = func() (string, error) {
			resolveCalls.Add(1)
			return root, nil
		}
		operations.prepareRoot = func(got string) error {
			if got != root {
				return fmt.Errorf("prepare root %q, want %q", got, root)
			}
			prepareCalls.Add(1)
			return nil
		}
		operations.apply = func(_ *exec.Cmd, launch launchProjection) error {
			applied = cloneExecutionPolicyTestLaunch(launch)
			Configure(&config.Config{Isolation: config.IsolationConfig{
				Enabled: false,
				ExposePaths: []config.ExposePath{{
					Source: "/global-b-source",
					Target: "/global-b-target",
					Mode:   "rw",
				}},
			}})
			return nil
		}
		operations.start = func(cmd *exec.Cmd) error {
			startCalls.Add(1)
			if len(cmd.Env) == 0 {
				return errors.New("projected environment is empty")
			}
			cmd.Env[0] = "MUTATED_AFTER_APPLY=1"
			return nil
		}
		operations.postStart = func(_ *exec.Cmd, launch launchProjection) error {
			postCalls.Add(1)
			posted = cloneExecutionPolicyTestLaunch(launch)
			return nil
		}
		t.Cleanup(func() { Configure(nil) })

		cmd := exec.Command("snapshot-command")
		cmd.Env = []string{"POLICY_CANARY=snapshot"}
		if err := startExecutionPolicy(policy, cmd, operations); err != nil {
			t.Fatalf("snapshot start error = %v", err)
		}
		if resolveCalls.Load() != 1 || prepareCalls.Load() != 1 ||
			startCalls.Load() != 1 || postCalls.Load() != 1 {
			t.Fatalf("launch calls resolve=%d prepare=%d start=%d post=%d",
				resolveCalls.Load(), prepareCalls.Load(), startCalls.Load(), postCalls.Load())
		}
		if !reflect.DeepEqual(applied, posted) {
			t.Fatalf("launch projection changed after apply:\napply=%#v\npost=%#v", applied, posted)
		}
		if !applied.isolation.Enabled || applied.root != root ||
			len(applied.isolation.ExposePaths) != 1 ||
			applied.isolation.ExposePaths[0].Source != "/snapshot-source" ||
			!executionPolicyTestEnvironmentContains(applied.environment, "POLICY_CANARY=snapshot") {
			t.Fatalf("retained launch = %#v", applied)
		}
		if executionPolicyTestEnvironmentContains(posted.environment, "MUTATED_AFTER_APPLY=1") {
			t.Fatalf("command environment mutation reached retained projection: %#v", posted.environment)
		}
	})

	for _, test := range []struct {
		name          string
		startErr      error
		postErr       error
		wantCleanup   int32
		wantPost      int32
		wantTerminate int32
	}{
		{
			name:        "start error cleans pending state",
			startErr:    errors.New("injected start failure"),
			wantCleanup: 1,
		},
		{
			name:          "post-start error terminates without caller wait",
			postErr:       errors.New("injected post-start failure"),
			wantPost:      1,
			wantTerminate: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var startCalls, cleanupCalls, postCalls, terminateCalls, waitCalls atomic.Int32
			operations := completeExecutionPolicyTestOperations("linux", "")
			operations.start = func(*exec.Cmd) error {
				startCalls.Add(1)
				return test.startErr
			}
			operations.cleanup = func(*exec.Cmd) { cleanupCalls.Add(1) }
			operations.postStart = func(*exec.Cmd, launchProjection) error {
				postCalls.Add(1)
				return test.postErr
			}
			operations.terminate = func(*exec.Cmd) { terminateCalls.Add(1) }
			operations.wait = func(*exec.Cmd) error {
				waitCalls.Add(1)
				return nil
			}
			wantErr := test.startErr
			if wantErr == nil {
				wantErr = test.postErr
			}
			err := runExecutionPolicy(
				NewExecutionPolicy(config.IsolationConfig{}),
				exec.Command("unused"),
				operations,
			)
			if !errors.Is(err, wantErr) || startCalls.Load() != 1 ||
				cleanupCalls.Load() != test.wantCleanup || postCalls.Load() != test.wantPost ||
				terminateCalls.Load() != test.wantTerminate || waitCalls.Load() != 0 {
				t.Fatalf("lifecycle error=%v start=%d cleanup=%d post=%d terminate=%d wait=%d",
					err, startCalls.Load(), cleanupCalls.Load(), postCalls.Load(),
					terminateCalls.Load(), waitCalls.Load())
			}
		})
	}

	t.Run("public start returns missing executable error", func(t *testing.T) {
		cmd := exec.Command(filepath.Join(t.TempDir(), "missing-executable"))
		if err := NewExecutionPolicy(config.IsolationConfig{}).Start(cmd); err == nil {
			t.Fatal("ExecutionPolicy.Start() accepted a missing executable")
		}
	})

	t.Run("platform apply error cleans pending state before start", func(t *testing.T) {
		applyErr := errors.New("injected platform apply failure")
		var cleanupCalls, startCalls atomic.Int32
		root := t.TempDir()
		operations := completeExecutionPolicyTestOperations("linux", root)
		operations.apply = func(*exec.Cmd, launchProjection) error { return applyErr }
		operations.cleanup = func(*exec.Cmd) { cleanupCalls.Add(1) }
		operations.start = func(*exec.Cmd) error {
			startCalls.Add(1)
			return nil
		}
		err := startExecutionPolicy(
			NewExecutionPolicy(config.IsolationConfig{Enabled: true}),
			exec.Command("unused"),
			operations,
		)
		if !errors.Is(err, applyErr) || cleanupCalls.Load() != 1 || startCalls.Load() != 0 {
			t.Fatalf("apply failure error=%v cleanup=%d start=%d",
				err, cleanupCalls.Load(), startCalls.Load())
		}
	})

	t.Run("real post-start failure kills and reaps child", func(t *testing.T) {
		postErr := errors.New("injected real post-start failure")
		readyPath := filepath.Join(t.TempDir(), "helper-ready")
		operations := defaultLaunchOperations()
		operations.postStart = func(*exec.Cmd, launchProjection) error {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(readyPath); err == nil {
					return postErr
				}
				time.Sleep(10 * time.Millisecond)
			}
			return fmt.Errorf("helper did not report readiness before post-start failure")
		}
		var outerWaitCalls atomic.Int32
		operations.wait = func(cmd *exec.Cmd) error {
			outerWaitCalls.Add(1)
			return cmd.Wait()
		}
		cmd := executionPolicyHelperCommandWithMode("block")
		cmd.Env = append(cmd.Env, executionPolicyReadyEnvironment+"="+readyPath)
		startedAt := time.Now()
		err := runExecutionPolicy(NewExecutionPolicy(config.IsolationConfig{}), cmd, operations)
		if !errors.Is(err, postErr) || outerWaitCalls.Load() != 0 {
			t.Fatalf("post-start error = %v, outer wait calls=%d", err, outerWaitCalls.Load())
		}
		if cmd.ProcessState == nil {
			t.Fatalf("post-start failure did not reap child: state=%v", cmd.ProcessState)
		}
		if cmd.ProcessState.Success() || time.Since(startedAt) >= 5*time.Second {
			t.Fatalf(
				"post-start failure did not promptly terminate blocking child: state=%v duration=%s",
				cmd.ProcessState,
				time.Since(startedAt),
			)
		}
		if secondWaitErr := cmd.Wait(); secondWaitErr == nil {
			t.Fatal("post-start failure left child available for a second Wait")
		}
	})

	for _, test := range []struct {
		name    string
		waitErr error
	}{
		{name: "success"},
		{name: "wait error", waitErr: errors.New("injected wait failure")},
	} {
		t.Run("run waits once "+test.name, func(t *testing.T) {
			var startCalls, postCalls, waitCalls atomic.Int32
			operations := completeExecutionPolicyTestOperations("linux", "")
			operations.start = func(*exec.Cmd) error {
				startCalls.Add(1)
				return nil
			}
			operations.postStart = func(*exec.Cmd, launchProjection) error {
				postCalls.Add(1)
				return nil
			}
			operations.wait = func(*exec.Cmd) error {
				waitCalls.Add(1)
				return test.waitErr
			}
			err := runExecutionPolicy(NewExecutionPolicy(config.IsolationConfig{}), exec.Command("unused"), operations)
			if !errors.Is(err, test.waitErr) || startCalls.Load() != 1 ||
				postCalls.Load() != 1 || waitCalls.Load() != 1 {
				t.Fatalf("run error=%v start=%d post=%d wait=%d",
					err, startCalls.Load(), postCalls.Load(), waitCalls.Load())
			}
		})
	}
}

func TestExecutionPolicyEnabledLinuxExplicitWiring(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux bubblewrap projection test")
	}
	root := t.TempDir()
	t.Setenv(config.EnvHome, root)
	binDir := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "bwrap-record")
	fakeBwrap := filepath.Join(binDir, "bwrap")
	fakeBody := `#!/bin/sh
{
  printf 'INVOCATION\n'
  printf 'ARG=%s\n' "$@"
  printf 'HOME=%s\n' "$HOME"
  printf 'CANARY=%s\n' "$P013_EXPLICIT_CANARY"
} >> "$P013_BWRAP_RECORD"
exit 0
`
	if err := os.WriteFile(fakeBwrap, []byte(fakeBody), 0o755); err != nil {
		t.Fatalf("write fake bwrap: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	exposure := t.TempDir()
	original := filepath.Join(t.TempDir(), "original-tool")
	if err := os.WriteFile(original, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatalf("write original tool: %v", err)
	}
	policy := NewExecutionPolicy(config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: exposure,
			Target: exposure,
			Mode:   "ro",
		}},
	})
	cmd := exec.Command(original, "--flag", "value")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"P013_BWRAP_RECORD="+recordPath,
		"P013_EXPLICIT_CANARY=retained",
	)
	if err := policy.Run(cmd); err != nil {
		t.Fatalf("enabled explicit policy Run() error = %v", err)
	}
	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read fake bwrap record: %v", err)
	}
	text := string(record)
	if count := strings.Count(text, "INVOCATION\n"); count != 1 {
		t.Fatalf("fake bwrap invocation count = %d, want 1:\n%s", count, text)
	}
	for _, want := range []string{
		"ARG=--bind\nARG=" + root + "\nARG=" + root,
		"ARG=--ro-bind\nARG=" + exposure + "\nARG=" + exposure,
		"ARG=--chdir\nARG=" + workDir,
		"ARG=--\nARG=" + original + "\nARG=--flag\nARG=value",
		"HOME=" + filepath.Join(root, "runtime-user-env", "home"),
		"CANARY=retained",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fake bwrap record missing %q:\n%s", want, text)
		}
	}
}

func TestExecutionPolicyEnabledLinuxMissingBwrapFailsClosed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux bubblewrap fail-closed test")
	}
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	cmd := executionPolicyHelperCommand()
	err := NewExecutionPolicy(config.IsolationConfig{Enabled: true}).Start(cmd)
	if err == nil || !strings.Contains(err.Error(), "requires bwrap") {
		t.Fatalf("missing bwrap error = %v", err)
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		t.Fatalf("missing bwrap started process: process=%v state=%v", cmd.Process, cmd.ProcessState)
	}
}

func TestExecutionPolicyConcurrentOppositePoliciesIgnoreConfigureChurn(t *testing.T) {
	root := t.TempDir()
	type policyCase struct {
		name     string
		policy   ExecutionPolicy
		enabled  bool
		exposure string
	}
	cases := []policyCase{
		{
			name:    "disabled",
			policy:  NewExecutionPolicy(config.IsolationConfig{}),
			enabled: false,
		},
		{
			name: "enabled-a",
			policy: NewExecutionPolicy(config.IsolationConfig{
				Enabled: true,
				ExposePaths: []config.ExposePath{{
					Source: "/concurrent-a-source",
					Target: "/concurrent-a-target",
					Mode:   "ro",
				}},
			}),
			enabled:  true,
			exposure: "/concurrent-a-source",
		},
		{
			name: "enabled-c",
			policy: NewExecutionPolicy(config.IsolationConfig{
				Enabled: true,
				ExposePaths: []config.ExposePath{{
					Source: "/concurrent-c-source",
					Target: "/concurrent-c-target",
					Mode:   "rw",
				}},
			}),
			enabled:  true,
			exposure: "/concurrent-c-source",
		},
	}
	legacyA := &config.Config{Isolation: config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/legacy-churn-a",
			Target: "/legacy-churn-a",
			Mode:   "ro",
		}},
	}}
	legacyB := &config.Config{Isolation: config.IsolationConfig{
		Enabled: false,
		ExposePaths: []config.ExposePath{{
			Source: "/legacy-churn-b",
			Target: "/legacy-churn-b",
			Mode:   "rw",
		}},
	}}
	Configure(legacyA)
	t.Cleanup(func() { Configure(nil) })

	start := make(chan struct{})
	errorsSeen := make(chan error, len(cases)*8)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for index := range 600 {
			if index%2 == 0 {
				Configure(legacyA)
			} else {
				Configure(legacyB)
			}
		}
	}()

	for _, test := range cases {
		for range 3 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				for range 30 {
					policyCopy := test.policy
					operations := completeExecutionPolicyTestOperations("linux", root)
					operations.apply = func(_ *exec.Cmd, launch launchProjection) error {
						if !test.enabled {
							return fmt.Errorf("%s reached apply while disabled", test.name)
						}
						return validateExecutionPolicyConcurrentLaunch(
							test.name,
							test.enabled,
							test.exposure,
							root,
							launch,
						)
					}
					operations.start = func(cmd *exec.Cmd) error {
						if !executionPolicyTestEnvironmentContains(
							cmd.Env,
							"POLICY_CASE="+test.name,
						) {
							return fmt.Errorf("%s command environment crossed: %#v", test.name, cmd.Env)
						}
						return nil
					}
					operations.postStart = func(_ *exec.Cmd, launch launchProjection) error {
						return validateExecutionPolicyConcurrentLaunch(
							test.name,
							test.enabled,
							test.exposure,
							root,
							launch,
						)
					}
					cmd := exec.Command("concurrent-" + test.name)
					cmd.Env = []string{"POLICY_CASE=" + test.name}
					if err := startExecutionPolicy(policyCopy, cmd, operations); err != nil {
						errorsSeen <- err
						return
					}
				}
			}()
		}
	}
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func executionPolicyHelperCommand() *exec.Cmd {
	return executionPolicyHelperCommandWithMode("run")
}

func executionPolicyHelperCommandWithMode(mode string) *exec.Cmd {
	cmd := exec.Command(
		os.Args[0],
		"-test.run=^TestExecutionPolicyHelperProcess$",
		"--",
		executionPolicyHelperArgument,
	)
	cmd.Env = append(os.Environ(), executionPolicyHelperEnvironment+"="+mode)
	return cmd
}

func completeExecutionPolicyTestOperations(goos, root string) launchOperations {
	return launchOperations{
		goos: goos,
		resolveRoot: func() (string, error) {
			return root, nil
		},
		prepareRoot: func(string) error { return nil },
		apply:       func(*exec.Cmd, launchProjection) error { return nil },
		start:       func(*exec.Cmd) error { return nil },
		postStart:   func(*exec.Cmd, launchProjection) error { return nil },
		cleanup:     func(*exec.Cmd) {},
		terminate:   func(*exec.Cmd) {},
		wait:        func(*exec.Cmd) error { return nil },
	}
}

func cloneExecutionPolicyTestLaunch(launch launchProjection) launchProjection {
	launch.isolation = cloneIsolationConfig(launch.isolation)
	launch.linuxBaseMounts = append([]MountRule(nil), launch.linuxBaseMounts...)
	launch.windowsAccess = append([]AccessRule(nil), launch.windowsAccess...)
	launch.environment = append([]string(nil), launch.environment...)
	return launch
}

func executionPolicyTestHasMount(haystack []MountRule, needle MountRule) bool {
	for _, rule := range haystack {
		if rule == needle {
			return true
		}
	}
	return false
}

func executionPolicyTestEnvironmentContains(environment []string, want string) bool {
	for _, item := range environment {
		if item == want {
			return true
		}
	}
	return false
}

func validateExecutionPolicyConcurrentLaunch(
	name string,
	enabled bool,
	exposure string,
	root string,
	launch launchProjection,
) error {
	if launch.isolation.Enabled != enabled {
		return fmt.Errorf("%s enabled=%v, want %v", name, launch.isolation.Enabled, enabled)
	}
	if !enabled {
		if launch.root != "" || len(launch.isolation.ExposePaths) != 0 {
			return fmt.Errorf("%s disabled projection crossed: %#v", name, launch)
		}
		return nil
	}
	if launch.root != root || len(launch.isolation.ExposePaths) != 1 ||
		launch.isolation.ExposePaths[0].Source != exposure {
		return fmt.Errorf("%s projection crossed: root=%q expose=%#v", name, launch.root, launch.isolation.ExposePaths)
	}
	if !executionPolicyTestEnvironmentContains(launch.environment, "POLICY_CASE="+name) {
		return fmt.Errorf("%s retained environment crossed: %#v", name, launch.environment)
	}
	return nil
}
