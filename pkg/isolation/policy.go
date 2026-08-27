package isolation

import (
	"errors"
	"os/exec"
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
	isolation config.IsolationConfig
}

// NewExecutionPolicy deeply detaches one ordered isolation configuration.
// Validation that matters only when isolation is enabled occurs at launch.
func NewExecutionPolicy(cfg config.IsolationConfig) ExecutionPolicy {
	return ExecutionPolicy{snapshot: &executionPolicySnapshot{
		isolation: cloneIsolationConfig(cfg),
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

func (policy ExecutionPolicy) detachedIsolation() (config.IsolationConfig, bool) {
	if policy.snapshot == nil {
		return config.IsolationConfig{}, false
	}
	return cloneIsolationConfig(policy.snapshot.isolation), true
}

func cloneIsolationConfig(cfg config.IsolationConfig) config.IsolationConfig {
	cloned := cfg
	if cfg.ExposePaths != nil {
		cloned.ExposePaths = make([]config.ExposePath, len(cfg.ExposePaths))
		copy(cloned.ExposePaths, cfg.ExposePaths)
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
