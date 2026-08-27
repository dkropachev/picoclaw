package isolation

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

// ErrExecutionPolicyUnavailable reports that an explicit execution policy was
// not constructed. The zero ExecutionPolicy value deliberately fails closed.
var ErrExecutionPolicyUnavailable = errors.New("execution policy unavailable")

// ExecutionPolicy is an immutable, safely copyable subprocess-isolation
// capability. Construct one with NewExecutionPolicy; its zero value is invalid.
type ExecutionPolicy struct {
	snapshot *executionPolicySnapshot
}

type executionPolicySnapshot struct {
	isolation   config.IsolationConfig
	environment capturedPolicyEnvironment
}

// NewExecutionPolicy deeply detaches one ordered isolation configuration and
// captures its admitted host environment once. Validation errors are retained
// and fail the first launch before command or process effects.
func NewExecutionPolicy(cfg config.IsolationConfig) ExecutionPolicy {
	detached := cloneIsolationConfig(cfg)
	return ExecutionPolicy{snapshot: &executionPolicySnapshot{
		isolation:   detached,
		environment: captureCurrentPolicyEnvironment(detached),
	}}
}

func newExecutionPolicyWithEnvironment(
	cfg config.IsolationConfig,
	ambient []string,
	goos string,
) ExecutionPolicy {
	detached := cloneIsolationConfig(cfg)
	return ExecutionPolicy{snapshot: &executionPolicySnapshot{
		isolation:   detached,
		environment: capturePolicyEnvironment(detached, ambient, goos),
	}}
}

// Start prepares and starts cmd with one exact policy and instance-root
// projection. Waiting remains the caller's responsibility.
func (policy ExecutionPolicy) Start(cmd *exec.Cmd) error {
	return startExecutionPolicy(policy, cmd, defaultLaunchOperations())
}

// Run starts and waits for cmd using the exact same projection as Start.
func (policy ExecutionPolicy) Run(cmd *exec.Cmd) error {
	return runExecutionPolicy(policy, cmd, defaultLaunchOperations())
}

// Validate reports construction-time environment, enabled-platform, exposure,
// and Linux-backend errors without filesystem or process effects. Root
// resolution/preparation and command-specific environment/executable checks
// remain part of Start and Run.
func (policy ExecutionPolicy) Validate() error {
	if policy.snapshot == nil {
		return ErrExecutionPolicyUnavailable
	}
	if policy.snapshot.environment.err != nil {
		return policy.snapshot.environment.err
	}
	if !policy.snapshot.isolation.Enabled {
		return nil
	}
	if !isSupportedOn(runtime.GOOS) {
		return fmt.Errorf("subprocess isolation is not supported on %s", runtime.GOOS)
	}
	if err := ValidateExposePaths(policy.snapshot.isolation.ExposePaths); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return validateWindowsExposePaths(policy.snapshot.isolation.ExposePaths)
	}
	if runtime.GOOS == "linux" {
		_, err := resolveExecutablePath(
			"bwrap",
			policy.snapshot.environment.hostPath,
			policy.snapshot.environment.hostPathExt,
			policy.snapshot.environment.hostPathExtPresent,
		)
		if err != nil {
			return linuxBackendUnavailableError(err)
		}
	}
	return nil
}

// LookupEnvironment returns one host value admitted and frozen when the policy
// was constructed. It never consults the live process environment. Values
// forced by enabled per-launch user-directory projection are intentionally not
// exposed here.
func (policy ExecutionPolicy) LookupEnvironment(name string) (string, bool) {
	if policy.snapshot == nil || policy.snapshot.environment.err != nil || name == "" {
		return "", false
	}
	canonical := canonicalEnvironmentKey(name, policy.snapshot.environment.goos)
	for _, item := range policy.snapshot.environment.allowed {
		key, value, ok := splitEnvironmentEntry(item)
		if ok && canonicalEnvironmentKey(key, policy.snapshot.environment.goos) == canonical {
			return value, true
		}
	}
	return "", false
}

// LookupCommandEnvironment projects one command environment without starting a
// process or creating directories. It is intended for trusted validation that
// must use the same captured and redirected values as Start.
func (policy ExecutionPolicy) LookupCommandEnvironment(
	name,
	dir string,
) (string, bool, error) {
	snapshot, ok := policy.detachedSnapshot()
	if !ok {
		return "", false, ErrExecutionPolicyUnavailable
	}
	if snapshot.environment.err != nil {
		return "", false, snapshot.environment.err
	}
	userEnv := UserEnv{}
	if snapshot.isolation.Enabled {
		root, err := ResolveInstanceRoot()
		if err != nil {
			return "", false, err
		}
		userEnv = ResolveUserEnv(root)
	}
	environment, err := restrictedEnvironmentForCommand(
		snapshot.environment,
		nil,
		dir,
		snapshot.isolation.Enabled,
		userEnv,
	)
	if err != nil {
		return "", false, err
	}
	value, present := environmentSliceLookup(
		environment,
		name,
		snapshot.environment.goos,
	)
	return value, present, nil
}

func (policy ExecutionPolicy) detachedIsolation() (config.IsolationConfig, bool) {
	if policy.snapshot == nil {
		return config.IsolationConfig{}, false
	}
	return cloneIsolationConfig(policy.snapshot.isolation), true
}

func (policy ExecutionPolicy) detachedSnapshot() (executionPolicySnapshot, bool) {
	if policy.snapshot == nil {
		return executionPolicySnapshot{}, false
	}
	return executionPolicySnapshot{
		isolation:   cloneIsolationConfig(policy.snapshot.isolation),
		environment: cloneCapturedPolicyEnvironment(policy.snapshot.environment),
	}, true
}

func cloneIsolationConfig(cfg config.IsolationConfig) config.IsolationConfig {
	cloned := cfg
	if cfg.ExposePaths != nil {
		cloned.ExposePaths = make([]config.ExposePath, len(cfg.ExposePaths))
		copy(cloned.ExposePaths, cfg.ExposePaths)
	}
	if cfg.EnvironmentAllowlist != nil {
		cloned.EnvironmentAllowlist = append(
			make([]string, 0, len(cfg.EnvironmentAllowlist)),
			cfg.EnvironmentAllowlist...,
		)
	}
	return cloned
}

var (
	isolationMu  sync.RWMutex
	legacyPolicy = NewExecutionPolicy(config.DefaultConfig().Isolation)
)

// Configure updates the process-wide compatibility policy used by subsequent
// legacy operations. The input is deeply detached before publication.
//
// Deprecated: construct an ExecutionPolicy and call its Start or Run method.
func Configure(cfg *config.Config) {
	isolationCfg := config.DefaultConfig().Isolation
	if cfg != nil {
		isolationCfg = cfg.Isolation
	}
	policy := NewExecutionPolicy(isolationCfg)

	isolationMu.Lock()
	legacyPolicy = policy
	isolationMu.Unlock()
}

// CurrentConfig returns a detached projection of the current compatibility
// policy.
//
// Deprecated: retain the caller-owned config used with NewExecutionPolicy.
func CurrentConfig() config.IsolationConfig {
	policy := currentLegacyPolicy()
	cfg, ok := policy.detachedIsolation()
	if !ok {
		return config.IsolationConfig{}
	}
	return cfg
}

func currentLegacyPolicy() ExecutionPolicy {
	isolationMu.RLock()
	policy := legacyPolicy
	isolationMu.RUnlock()
	return policy
}
