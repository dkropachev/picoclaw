package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

type hookRuntime struct {
	initMu         sync.Mutex
	initOnce       sync.Once
	mu             sync.Mutex
	initErr        error
	mounted        []hookRuntimeMount
	closed         bool
	beforeInitLock func()
}

type hookRuntimeMount struct {
	name    string
	mountID uint64
}

func (r *hookRuntime) setInitErr(err error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.initErr = err
	r.mu.Unlock()
}

func (r *hookRuntime) getInitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initErr
}

func (r *hookRuntime) setMounted(mounts []hookRuntimeMount) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.mounted = append([]hookRuntimeMount(nil), mounts...)
	r.mu.Unlock()
}

func (r *hookRuntime) reset(al *AgentLoop) {
	// Serialize reset with lazy initialization. A reload that publishes B while
	// an out-of-band A initialization is finishing must wait, unmount all of A,
	// then allow B initialization; sync.Once itself cannot race with reset.
	if r.beforeInitLock != nil {
		r.beforeInitLock()
	}
	r.initMu.Lock()
	defer r.initMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	mounts := append([]hookRuntimeMount(nil), r.mounted...)
	r.mounted = nil
	r.initErr = nil
	r.initOnce = sync.Once{}
	r.mu.Unlock()

	for _, mount := range mounts {
		al.hooks.unmountTracked(mount.name, mount.mountID)
	}
}

func (r *hookRuntime) close(al *AgentLoop) {
	if r == nil {
		return
	}
	if r.beforeInitLock != nil {
		r.beforeInitLock()
	}
	r.initMu.Lock()
	r.mu.Lock()
	r.closed = true
	r.mounted = nil
	r.initErr = fmt.Errorf("hook runtime is closed")
	r.mu.Unlock()
	r.initMu.Unlock()
	if al != nil && al.hooks != nil {
		al.hooks.Close()
	}
}

// BuiltinHookFactory constructs an in-process hook from config.
type BuiltinHookFactory func(ctx context.Context, spec config.BuiltinHookConfig) (any, error)

var (
	builtinHookRegistryMu sync.RWMutex
	builtinHookRegistry   = map[string]BuiltinHookFactory{}
)

// RegisterBuiltinHook registers a named in-process hook factory for config-driven mounting.
func RegisterBuiltinHook(name string, factory BuiltinHookFactory) error {
	if name == "" {
		return fmt.Errorf("builtin hook name is required")
	}
	if factory == nil {
		return fmt.Errorf("builtin hook %q factory is nil", name)
	}

	builtinHookRegistryMu.Lock()
	defer builtinHookRegistryMu.Unlock()

	if _, exists := builtinHookRegistry[name]; exists {
		return fmt.Errorf("builtin hook %q is already registered", name)
	}
	builtinHookRegistry[name] = factory
	return nil
}

func unregisterBuiltinHook(name string) {
	if name == "" {
		return
	}
	builtinHookRegistryMu.Lock()
	delete(builtinHookRegistry, name)
	builtinHookRegistryMu.Unlock()
}

func lookupBuiltinHook(name string) (BuiltinHookFactory, bool) {
	builtinHookRegistryMu.RLock()
	defer builtinHookRegistryMu.RUnlock()

	factory, ok := builtinHookRegistry[name]
	return factory, ok
}

func configureHookManagerFromConfig(hm *HookManager, cfg *config.Config) {
	if hm == nil || cfg == nil {
		return
	}
	hm.ConfigureTimeouts(
		hookTimeoutFromMS(cfg.Hooks.Defaults.ObserverTimeoutMS),
		hookTimeoutFromMS(cfg.Hooks.Defaults.InterceptorTimeoutMS),
		hookTimeoutFromMS(cfg.Hooks.Defaults.ApprovalTimeoutMS),
	)
}

func hookTimeoutFromMS(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (al *AgentLoop) ensureHooksInitialized(ctx context.Context) error {
	if al == nil {
		return nil
	}
	al.mu.RLock()
	cfg := al.cfg
	al.mu.RUnlock()
	if !hookGenerationNeedsRuntimeLease(cfg) {
		// Preserve the no-op path without depending on runtime admission. Recheck
		// after taking the generation mutex: a reload may have published enabled
		// hooks after the first snapshot.
		al.hookGenerationMu.Lock()
		al.mu.RLock()
		cfg = al.cfg
		al.mu.RUnlock()
		if !hookGenerationNeedsRuntimeLease(cfg) {
			defer al.hookGenerationMu.Unlock()
			return al.ensureHooksInitializedForGeneration(ctx, cfg)
		}
		al.hookGenerationMu.Unlock()
	}

	// Own the current runtime generation before taking the hook-generation
	// mutex. Configured factories may re-enter runtime-admitted Agent APIs; a
	// concurrent reload must wait for that work to finish instead of pausing
	// admission and then deadlocking on this mutex.
	leaseCtx, releaseRuntime, err := al.acquireTrustedRuntimeRoot(ctx)
	if err != nil {
		return err
	}
	defer releaseRuntime()

	al.hookGenerationMu.Lock()
	defer al.hookGenerationMu.Unlock()
	al.mu.RLock()
	cfg = al.cfg
	al.mu.RUnlock()
	return al.ensureHooksInitializedForGeneration(leaseCtx, cfg)
}

func hookGenerationNeedsRuntimeLease(cfg *config.Config) bool {
	if cfg == nil || !cfg.Hooks.Enabled {
		return false
	}
	return len(enabledBuiltinHookNames(cfg.Hooks.Builtins)) > 0 ||
		len(enabledProcessHookNames(cfg.Hooks.Processes)) > 0
}

func (al *AgentLoop) ensureHooksInitializedForGeneration(
	ctx context.Context,
	cfg *config.Config,
) error {
	if al == nil || al.hooks == nil {
		return nil
	}

	al.hookRuntime.initMu.Lock()
	defer al.hookRuntime.initMu.Unlock()
	al.hookRuntime.mu.Lock()
	closed := al.hookRuntime.closed
	al.hookRuntime.mu.Unlock()
	if closed {
		return fmt.Errorf("hook runtime is closed")
	}
	al.hookRuntime.initOnce.Do(func() {
		al.hookRuntime.setInitErr(al.loadConfiguredHooks(ctx, cfg))
	})

	return al.hookRuntime.getInitErr()
}

func (al *AgentLoop) loadConfiguredHooks(
	ctx context.Context,
	cfg *config.Config,
) (err error) {
	if al == nil {
		return nil
	}
	if cfg == nil || !cfg.Hooks.Enabled {
		return nil
	}

	processNames := enabledProcessHookNames(cfg.Hooks.Processes)
	var executionPolicy isolation.ExecutionPolicy
	if len(processNames) > 0 {
		executionPolicy, err = al.ExecutionPolicyForGeneration(cfg)
		if err != nil {
			return fmt.Errorf("snapshot configured hook execution policy: %w", err)
		}
	}

	mounted := make([]hookRuntimeMount, 0)
	defer func() {
		if err != nil {
			for _, mount := range mounted {
				al.hooks.unmountTracked(mount.name, mount.mountID)
			}
			return
		}
		al.hookRuntime.setMounted(mounted)
	}()

	builtinNames := enabledBuiltinHookNames(cfg.Hooks.Builtins)
	for _, name := range builtinNames {
		spec := cfg.Hooks.Builtins[name]
		factory, ok := lookupBuiltinHook(name)
		if !ok {
			return fmt.Errorf("builtin hook %q is not registered", name)
		}

		hook, factoryErr := factory(ctx, spec)
		if factoryErr != nil {
			return fmt.Errorf("build builtin hook %q: %w", name, factoryErr)
		}
		mountID, mountErr := al.hooks.mountTracked(HookRegistration{
			Name:     name,
			Priority: spec.Priority,
			Source:   HookSourceInProcess,
			Trust:    HookTrustTrusted,
			Hook:     hook,
		})
		if mountErr != nil {
			closeHookIfPossible(hook)
			return fmt.Errorf("mount builtin hook %q: %w", name, mountErr)
		}
		mounted = append(mounted, hookRuntimeMount{name: name, mountID: mountID})
	}

	for _, name := range processNames {
		spec := cfg.Hooks.Processes[name]
		opts, buildErr := processHookOptionsFromConfig(spec)
		if buildErr != nil {
			return fmt.Errorf("configure process hook %q: %w", name, buildErr)
		}

		processHook, buildErr := NewProcessHookWithExecutionPolicy(
			ctx,
			name,
			opts,
			executionPolicy,
		)
		if buildErr != nil {
			return fmt.Errorf("start process hook %q: %w", name, buildErr)
		}
		mountID, mountErr := al.hooks.mountTracked(HookRegistration{
			Name:     name,
			Priority: spec.Priority,
			Source:   HookSourceProcess,
			Trust:    hookTrustFromBool(opts.Trusted),
			Hook:     processHook,
		})
		if mountErr != nil {
			_ = processHook.Close()
			return fmt.Errorf("mount process hook %q: %w", name, mountErr)
		}
		mounted = append(mounted, hookRuntimeMount{name: name, mountID: mountID})
	}

	return nil
}

func enabledBuiltinHookNames(specs map[string]config.BuiltinHookConfig) []string {
	if len(specs) == 0 {
		return nil
	}

	names := make([]string, 0, len(specs))
	for name, spec := range specs {
		if spec.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func enabledProcessHookNames(specs map[string]config.ProcessHookConfig) []string {
	if len(specs) == 0 {
		return nil
	}

	names := make([]string, 0, len(specs))
	for name, spec := range specs {
		if spec.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func processHookOptionsFromConfig(spec config.ProcessHookConfig) (ProcessHookOptions, error) {
	transport := spec.Transport
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" {
		return ProcessHookOptions{}, fmt.Errorf("unsupported transport %q", transport)
	}
	if len(spec.Command) == 0 {
		return ProcessHookOptions{}, fmt.Errorf("command is required")
	}

	opts := ProcessHookOptions{
		Command: append([]string(nil), spec.Command...),
		Dir:     spec.Dir,
		Env:     processHookEnvFromMap(spec.Env),
		Trusted: spec.Trusted,
	}

	observeKinds, observeEnabled, err := processHookObserveKindsFromConfig(spec.Observe)
	if err != nil {
		return ProcessHookOptions{}, err
	}
	opts.Observe = observeEnabled
	opts.ObserveKinds = observeKinds

	for _, intercept := range spec.Intercept {
		switch intercept {
		case "before_llm", "after_llm":
			opts.InterceptLLM = true
		case "before_tool", "after_tool":
			opts.InterceptTool = true
		case "approve_tool":
			opts.ApproveTool = true
		case "":
			continue
		default:
			return ProcessHookOptions{}, fmt.Errorf("unsupported intercept %q", intercept)
		}
	}

	if !opts.Observe && !opts.InterceptLLM && !opts.InterceptTool && !opts.ApproveTool {
		return ProcessHookOptions{}, fmt.Errorf("no hook modes enabled")
	}

	return opts, nil
}

func processHookEnvFromMap(envMap map[string]string) []string {
	if len(envMap) == 0 {
		return nil
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

func processHookObserveKindsFromConfig(observe []string) ([]string, bool, error) {
	if len(observe) == 0 {
		return nil, false, nil
	}

	validKinds := validHookEventKinds()
	normalized := make([]string, 0, len(observe))
	for _, kind := range observe {
		switch kind {
		case "", "*", "all":
			return nil, true, nil
		default:
			normalizedKind, ok := validKinds[kind]
			if !ok {
				return nil, false, fmt.Errorf("unsupported observe event %q", kind)
			}
			normalized = append(normalized, normalizedKind)
		}
	}

	if len(normalized) == 0 {
		return nil, false, nil
	}
	return normalized, true, nil
}

func validHookEventKinds() map[string]string {
	runtimeKinds := runtimeevents.KnownKinds()
	kinds := make(map[string]string, len(runtimeKinds)*2)
	for _, kind := range runtimeKinds {
		kinds[kind.String()] = kind.String()
	}
	kinds["turn_start"] = runtimeevents.KindAgentTurnStart.String()
	kinds["turn_end"] = runtimeevents.KindAgentTurnEnd.String()
	kinds["llm_request"] = runtimeevents.KindAgentLLMRequest.String()
	kinds["llm_delta"] = runtimeevents.KindAgentLLMDelta.String()
	kinds["llm_response"] = runtimeevents.KindAgentLLMResponse.String()
	kinds["llm_retry"] = runtimeevents.KindAgentLLMRetry.String()
	kinds["context_compress"] = runtimeevents.KindAgentContextCompress.String()
	kinds["session_summarize"] = runtimeevents.KindAgentSessionSummarize.String()
	kinds["tool_exec_start"] = runtimeevents.KindAgentToolExecStart.String()
	kinds["tool_exec_end"] = runtimeevents.KindAgentToolExecEnd.String()
	kinds["tool_exec_skipped"] = runtimeevents.KindAgentToolExecSkipped.String()
	kinds["tool_policy_decision"] = runtimeevents.KindAgentToolPolicyDecision.String()
	kinds["steering_injected"] = runtimeevents.KindAgentSteeringInjected.String()
	kinds["follow_up_queued"] = runtimeevents.KindAgentFollowUpQueued.String()
	kinds["interrupt_received"] = runtimeevents.KindAgentInterruptReceived.String()
	kinds["subturn_spawn"] = runtimeevents.KindAgentSubTurnSpawn.String()
	kinds["subturn_end"] = runtimeevents.KindAgentSubTurnEnd.String()
	kinds["subturn_result_delivered"] = runtimeevents.KindAgentSubTurnResultDelivered.String()
	kinds["subturn_orphan"] = runtimeevents.KindAgentSubTurnOrphan.String()
	kinds["error"] = runtimeevents.KindAgentError.String()
	return kinds
}
