package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

type builtinAutoHookConfig struct {
	Model  string `json:"model"`
	Suffix string `json:"suffix"`
}

type builtinAutoHook struct {
	model  string
	suffix string
}

func (h *builtinAutoHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Model = h.model
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *builtinAutoHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	next := resp.Clone()
	if next.Response != nil {
		next.Response.Content += h.suffix
	}
	return next, HookDecision{Action: HookActionModify}, nil
}

func newConfiguredHookLoop(t *testing.T, provider *llmHookTestProvider, hooks config.HooksConfig) *AgentLoop {
	t.Helper()
	cfg := newConfiguredHookConfig(t, hooks)
	return newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
}

func newConfiguredHookConfig(t *testing.T, hooks config.HooksConfig) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "builtin-model", Model: "builtin-model"},
			{Name: "process-model", Model: "process-model"},
		},
		Hooks: hooks,
	}
}

func TestAgentLoop_ProcessDirectWithChannel_AutoMountsBuiltinHook(t *testing.T) {
	const hookName = "test-auto-builtin-hook"

	if err := RegisterBuiltinHook(hookName, func(
		ctx context.Context,
		spec config.BuiltinHookConfig,
	) (any, error) {
		var hookCfg builtinAutoHookConfig
		if len(spec.Config) > 0 {
			if err := json.Unmarshal(spec.Config, &hookCfg); err != nil {
				return nil, err
			}
		}
		return &builtinAutoHook{
			model:  hookCfg.Model,
			suffix: hookCfg.Suffix,
		}, nil
	}); err != nil {
		t.Fatalf("RegisterBuiltinHook failed: %v", err)
	}
	t.Cleanup(func() {
		unregisterBuiltinHook(hookName)
	})

	rawCfg, err := json.Marshal(builtinAutoHookConfig{
		Model:  "builtin-model",
		Suffix: "|builtin",
	})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	provider := &llmHookTestProvider{}
	al := newConfiguredHookLoop(t, provider, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {
				Enabled: true,
				Config:  rawCfg,
			},
		},
	})
	defer al.Close()

	resp, err := al.ProcessDirectWithChannel(context.Background(), "hello", "session-1", "cli", "direct")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if resp != "provider content|builtin" {
		t.Fatalf("expected builtin-hooked content, got %q", resp)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "builtin-model" {
		t.Fatalf("expected builtin model, got %q", lastModel)
	}
}

func TestHookRuntimeResetSerializesInFlightInitialization(t *testing.T) {
	const hookName = "test-reset-serialization-hook"
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := RegisterBuiltinHook(hookName, func(
		context.Context,
		config.BuiltinHookConfig,
	) (any, error) {
		started <- struct{}{}
		<-release
		return struct{}{}, nil
	}); err != nil {
		t.Fatalf("RegisterBuiltinHook() error = %v", err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })

	al := newConfiguredHookLoop(t, &llmHookTestProvider{}, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	})
	defer al.Close()

	initDone := make(chan error, 1)
	go func() {
		initDone <- al.ensureHooksInitialized(context.Background())
	}()
	<-started

	resetDone := make(chan struct{})
	resetStarted := make(chan struct{})
	var resetStartOnce sync.Once
	al.hookRuntime.beforeInitLock = func() {
		resetStartOnce.Do(func() { close(resetStarted) })
	}
	go func() {
		al.hookRuntime.reset(al)
		close(resetDone)
	}()
	<-resetStarted
	select {
	case <-resetDone:
		close(release)
		<-initDone
		t.Fatal("hook runtime reset overtook in-flight initialization")
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("ensureHooksInitialized() error = %v", err)
	}
	<-resetDone
	if hooks := al.hooks.snapshotHooks(); len(hooks) != 0 {
		t.Fatalf("reset left %d hook registrations, want none", len(hooks))
	}

	if err := al.ensureHooksInitialized(context.Background()); err != nil {
		t.Fatalf("ensureHooksInitialized() after reset error = %v", err)
	}
	if hooks := al.hooks.snapshotHooks(); len(hooks) != 1 || hooks[0].Name != hookName {
		t.Fatalf("post-reset hooks = %+v, want only %q", hooks, hookName)
	}
}

func TestHookInitializationOwnsRuntimeBeforeGenerationMutex(t *testing.T) {
	const hookName = "test-hook-runtime-lease-order"
	factoryStarted := make(chan struct{})
	allowReentry := make(chan struct{})
	reentryDone := make(chan error, 1)
	var al *AgentLoop
	var cfg *config.Config
	if err := RegisterBuiltinHook(hookName, func(
		ctx context.Context,
		_ config.BuiltinHookConfig,
	) (any, error) {
		close(factoryStarted)
		<-allowReentry
		_, release, err := al.AcquireRuntimeGeneration(ctx, cfg)
		if err == nil {
			release()
		}
		reentryDone <- err
		return &builtinAutoHook{}, err
	}); err != nil {
		t.Fatalf("RegisterBuiltinHook() error = %v", err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })

	al = newConfiguredHookLoop(t, &llmHookTestProvider{}, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	})
	cfg = al.GetConfig()
	defer al.Close()

	initDone := make(chan error, 1)
	go func() {
		initDone <- al.ensureHooksInitialized(context.Background())
	}()
	<-factoryStarted

	pauseReturned := make(chan func(), 1)
	pauseDone := make(chan error, 1)
	go func() {
		resume, err := al.PauseRuntimeForReload(context.Background())
		if err != nil {
			pauseDone <- err
			return
		}
		pauseReturned <- resume
		al.hookGenerationMu.Lock()
		al.hookGenerationMu.Unlock()
		resume()
		pauseDone <- nil
	}()

	deadline := time.Now().Add(time.Second)
	var activeRuntimeUses int
	for {
		al.runtimeGateMu.Lock()
		paused := al.runtimeGatePaused
		activeRuntimeUses = al.runtimeGateActive
		al.runtimeGateMu.Unlock()
		if paused {
			break
		}
		if time.Now().After(deadline) {
			close(allowReentry)
			t.Fatal("reload did not begin pausing runtime admission")
		}
		time.Sleep(time.Millisecond)
	}

	close(allowReentry)
	if activeRuntimeUses == 0 {
		resume := <-pauseReturned
		resume()
		<-reentryDone
		<-initDone
		<-pauseDone
		t.Fatal("reload paused without hook initialization owning a runtime use")
	}
	select {
	case err := <-reentryDone:
		if err != nil {
			t.Fatalf("factory runtime re-entry error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hook factory runtime re-entry deadlocked with reload")
	}
	if err := <-initDone; err != nil {
		t.Fatalf("ensureHooksInitialized() error = %v", err)
	}
	resume := <-pauseReturned
	resume()
	if err := <-pauseDone; err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
}

func TestReloadHookInitializationOwnsPausedRuntimeContext(t *testing.T) {
	const hookName = "test-reload-hook-runtime-owner"
	reentryDone := make(chan error, 1)
	var al *AgentLoop
	var cfgB *config.Config
	if err := RegisterBuiltinHook(hookName, func(
		ctx context.Context,
		_ config.BuiltinHookConfig,
	) (any, error) {
		if runtimeLeaseOwner(ctx) != al {
			err := errors.New("reload hook factory received an unowned runtime context")
			reentryDone <- err
			return nil, err
		}
		_, release, err := al.AcquireRuntimeGeneration(ctx, cfgB)
		if err == nil {
			release()
		}
		reentryDone <- err
		return &builtinAutoHook{}, err
	}); err != nil {
		t.Fatalf("RegisterBuiltinHook() error = %v", err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })

	cfgA := newConfiguredHookConfig(t, config.HooksConfig{})
	cfgB = newConfiguredHookConfig(t, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	})
	msgBus := bus.NewMessageBus()
	al = newTestAgentLoopWithStrictModels(cfgA, msgBus, &llmHookTestProvider{})
	defer func() {
		al.Close()
		msgBus.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := al.ReloadProviderAndConfigWithExecutionPolicy(
		ctx,
		&llmHookTestProvider{},
		cfgB,
		isolation.NewExecutionPolicy(cfgB.Isolation),
	); err != nil {
		t.Fatalf("ReloadProviderAndConfigWithExecutionPolicy() error = %v", err)
	}
	select {
	case err := <-reentryDone:
		if err != nil {
			t.Fatalf("reload hook factory runtime re-entry error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reload hook factory did not attempt runtime re-entry")
	}
}

func TestAgentLoopCloseJoinsInFlightHookInitialization(t *testing.T) {
	const hookName = "test-close-serialization-hook"
	started := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	if err := RegisterBuiltinHook(hookName, func(
		context.Context,
		config.BuiltinHookConfig,
	) (any, error) {
		close(started)
		<-release
		return &closeTrackingHook{closed: closed}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })

	al := newConfiguredHookLoop(t, &llmHookTestProvider{}, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	})
	initDone := make(chan error, 1)
	go func() { initDone <- al.ensureHooksInitialized(context.Background()) }()
	<-started
	closeDone := make(chan struct{})
	closeStarted := make(chan struct{})
	var closeStartOnce sync.Once
	al.hookRuntime.beforeInitLock = func() {
		closeStartOnce.Do(func() { close(closeStarted) })
	}
	go func() {
		al.Close()
		close(closeDone)
	}()
	<-closeStarted
	select {
	case <-closeDone:
		close(release)
		t.Fatal("AgentLoop.Close overtook hook initialization")
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("hook initialization error = %v", err)
	}
	<-closeDone
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("configured hook was not closed")
	}
	if err := al.ensureHooksInitialized(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("post-close initialization error = %v", err)
	}
}

func TestConfiguredBuiltinCollisionClosesCandidateAndPreservesManual(t *testing.T) {
	const hookName = "p014-manual-builtin-collision"
	closed := make(chan struct{})
	if err := RegisterBuiltinHook(hookName, func(
		context.Context,
		config.BuiltinHookConfig,
	) (any, error) {
		return &closeTrackingHook{closed: closed}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })
	al := newConfiguredHookLoop(t, &llmHookTestProvider{}, config.HooksConfig{})
	defer al.Close()
	manual := &builtinAutoHook{}
	if err := al.MountHook(NamedHook(hookName, manual)); err != nil {
		t.Fatal(err)
	}
	cfg := newConfiguredHookConfig(t, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	})
	err := al.loadConfiguredHooks(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "collides with a manual hook") {
		t.Fatalf("configured/manual collision error = %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("rejected configured builtin candidate was not closed")
	}
	registrations := al.hooks.snapshotHooks()
	if len(registrations) != 1 || registrations[0].Hook != manual {
		t.Fatalf("manual registration was replaced: %#v", registrations)
	}
}

type closeTrackingHook struct {
	closed chan struct{}
	once   sync.Once
}

func (h *closeTrackingHook) Close() error {
	h.once.Do(func() { close(h.closed) })
	return nil
}

func TestAgentLoop_ProcessDirectWithChannel_AutoMountsProcessHook(t *testing.T) {
	provider := &llmHookTestProvider{}
	eventLog := filepath.Join(t.TempDir(), "events.log")

	al := newConfiguredHookLoop(t, provider, config.HooksConfig{
		Enabled: true,
		Processes: map[string]config.ProcessHookConfig{
			"ipc-auto": {
				Enabled:   true,
				Trusted:   true,
				Command:   processHookHelperCommand(),
				Env:       processHookHelperEnvMap("rewrite", eventLog),
				Observe:   []string{"turn_end"},
				Intercept: []string{"before_llm", "after_llm"},
			},
		},
	})
	defer al.Close()

	resp, err := al.ProcessDirectWithChannel(context.Background(), "hello", "session-1", "cli", "direct")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if resp != "provider content|ipc" {
		t.Fatalf("expected process-hooked content, got %q", resp)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "process-model" {
		t.Fatalf("expected process model, got %q", lastModel)
	}

	waitForFileContains(t, eventLog, "agent.turn.end")
}

func TestAgentLoop_ConfiguredProcessHookUsesExactPolicyAcrossReload(t *testing.T) {
	const policyKey = "P014_CONFIGURED_HOOK_GENERATION"
	isolationCfg := config.DefaultConfig().Isolation
	isolationCfg.EnvironmentAllowlist = []string{policyKey}

	t.Setenv(policyKey, "generation-a")
	policyA := isolation.NewExecutionPolicy(isolationCfg)
	reportA := filepath.Join(t.TempDir(), "generation-a.json")
	cfgA := newConfiguredHookConfig(
		t,
		configuredProcessHookEnvironmentReport(reportA, policyKey),
	)
	cfgA.Isolation = isolationCfg
	al := newTestAgentLoopWithStrictModelsAndExecutionPolicy(
		cfgA,
		bus.NewMessageBus(),
		&llmHookTestProvider{},
		policyA,
	)
	defer al.Close()

	if err := al.ensureHooksInitialized(context.Background()); err != nil {
		t.Fatalf("ensureHooksInitialized() for generation A error = %v", err)
	}
	hookA := configuredProcessHookByName(t, al, "ipc-auto")
	gotA := readProcessHookEnvironmentReport(t, reportA)[policyKey]
	if !gotA.Present || gotA.Value != "generation-a" {
		t.Fatalf("configured generation A environment = %+v", gotA)
	}

	t.Setenv(policyKey, "generation-b")
	policyB := isolation.NewExecutionPolicy(isolationCfg)
	reportB := filepath.Join(t.TempDir(), "generation-b.json")
	cfgB := newConfiguredHookConfig(
		t,
		configuredProcessHookEnvironmentReport(reportB, policyKey),
	)
	cfgB.Isolation = isolationCfg
	if err := al.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&llmHookTestProvider{},
		cfgB,
		policyB,
	); err != nil {
		t.Fatalf("ReloadProviderAndConfigWithExecutionPolicy() error = %v", err)
	}

	hookB := configuredProcessHookByName(t, al, "ipc-auto")
	if hookB == hookA {
		t.Fatal("configured process hook was not replaced across reload")
	}
	if !hookA.closed.Load() {
		t.Fatal("configured generation A hook remained open after reload")
	}
	gotB := readProcessHookEnvironmentReport(t, reportB)[policyKey]
	if !gotB.Present || gotB.Value != "generation-b" {
		t.Fatalf("configured generation B environment = %+v", gotB)
	}
}

func TestAgentLoop_ManualProcessHookRetainsLaunchPolicyAcrossReload(t *testing.T) {
	const policyKey = "P014_MANUAL_HOOK_GENERATION"
	isolationCfg := config.DefaultConfig().Isolation
	isolationCfg.EnvironmentAllowlist = []string{policyKey}

	t.Setenv(policyKey, "generation-a")
	policyA := isolation.NewExecutionPolicy(isolationCfg)
	provider := &llmHookTestProvider{}
	al, _, cleanup := newHookTestLoop(t, provider, policyA)
	defer cleanup()

	reportA := filepath.Join(t.TempDir(), "manual-a.json")
	envA := processHookHelperEnv("rewrite", "")
	envA = append(envA, processHookEnvironmentReportEnv(reportA, policyKey)...)
	if err := al.MountProcessHook(context.Background(), "manual-a", ProcessHookOptions{
		Command:      processHookHelperCommand(),
		Env:          envA,
		InterceptLLM: true,
	}); err != nil {
		t.Fatalf("MountProcessHook() generation A error = %v", err)
	}
	hookA := configuredProcessHookByName(t, al, "manual-a")

	t.Setenv(policyKey, "generation-b")
	policyB := isolation.NewExecutionPolicy(isolationCfg)
	cfgB := newConfiguredHookConfig(t, config.HooksConfig{
		Enabled: true,
		Processes: map[string]config.ProcessHookConfig{
			"manual-a": {
				Enabled:   true,
				Command:   processHookHelperCommand(),
				Env:       processHookHelperEnvMap("rewrite", ""),
				Intercept: []string{"before_llm"},
			},
		},
	})
	cfgB.Isolation = isolationCfg
	if err := al.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&llmHookTestProvider{},
		cfgB,
		policyB,
	); err != nil {
		t.Fatalf("ReloadProviderAndConfigWithExecutionPolicy() error = %v", err)
	}

	if got := configuredProcessHookByName(t, al, "manual-a"); got != hookA {
		t.Fatal("manual process hook was replaced across reload")
	}
	if hookA.closed.Load() {
		t.Fatal("manual generation A hook was closed across reload")
	}
	gotA := readProcessHookEnvironmentReport(t, reportA)[policyKey]
	if !gotA.Present || gotA.Value != "generation-a" {
		t.Fatalf("manual generation A environment = %+v", gotA)
	}

	reportB := filepath.Join(t.TempDir(), "manual-b.json")
	envB := processHookHelperEnv("rewrite", "")
	envB = append(envB, processHookEnvironmentReportEnv(reportB, policyKey)...)
	if err := al.MountProcessHook(context.Background(), "manual-b", ProcessHookOptions{
		Command:      processHookHelperCommand(),
		Env:          envB,
		InterceptLLM: true,
	}); err != nil {
		t.Fatalf("MountProcessHook() generation B error = %v", err)
	}
	gotB := readProcessHookEnvironmentReport(t, reportB)[policyKey]
	if !gotB.Present || gotB.Value != "generation-b" {
		t.Fatalf("manual generation B environment = %+v", gotB)
	}
}

func configuredProcessHookEnvironmentReport(
	reportPath string,
	keys ...string,
) config.HooksConfig {
	env := processHookHelperEnvMap("rewrite", "")
	env[processHookHelperEnvReportPath] = reportPath
	env[processHookHelperEnvReportKeys] = strings.Join(keys, ",")
	return config.HooksConfig{
		Enabled: true,
		Processes: map[string]config.ProcessHookConfig{
			"ipc-auto": {
				Enabled:   true,
				Command:   processHookHelperCommand(),
				Env:       env,
				Intercept: []string{"before_llm"},
			},
		},
	}
}

func configuredProcessHookByName(
	t *testing.T,
	al *AgentLoop,
	name string,
) *ProcessHook {
	t.Helper()
	for _, registration := range al.hooks.snapshotHooks() {
		if registration.Name != name {
			continue
		}
		hook, ok := registration.Hook.(*ProcessHook)
		if !ok {
			t.Fatalf("hook %q has type %T, want *ProcessHook", name, registration.Hook)
		}
		return hook
	}
	t.Fatalf("process hook %q is not mounted", name)
	return nil
}

func TestProcessHookObserveKindsFromConfigAcceptsRuntimeNames(t *testing.T) {
	kinds, enabled, err := processHookObserveKindsFromConfig([]string{
		"tool_exec_start",
		"agent.tool.exec_end",
		"tool_policy_decision",
		"agent.tool.policy_decision",
		"gateway.ready",
		"mcp.server.failed",
	})
	if err != nil {
		t.Fatalf("processHookObserveKindsFromConfig failed: %v", err)
	}
	if !enabled {
		t.Fatal("expected observe to be enabled")
	}

	want := []string{
		"agent.tool.exec_start",
		"agent.tool.exec_end",
		"agent.tool.policy_decision",
		"agent.tool.policy_decision",
		"gateway.ready",
		"mcp.server.failed",
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("observe kinds = %v, want %v", kinds, want)
	}
}

func TestProcessHookOptionsFromConfigRequiresExplicitTrust(t *testing.T) {
	for _, test := range []struct {
		name    string
		trusted bool
	}{
		{name: "default untrusted"},
		{name: "explicit trusted", trusted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, err := processHookOptionsFromConfig(config.ProcessHookConfig{
				Enabled:   true,
				Trusted:   test.trusted,
				Command:   []string{"hook"},
				Intercept: []string{"before_tool"},
			})
			if err != nil {
				t.Fatalf("processHookOptionsFromConfig() error = %v", err)
			}
			if opts.Trusted != test.trusted {
				t.Fatalf("Trusted = %v, want %v", opts.Trusted, test.trusted)
			}
		})
	}
}

func TestProcessHookEnvFromMapIsSortedDetachedAndPreservesEmpty(t *testing.T) {
	source := map[string]string{
		"Z_EMPTY": "",
		"A_VALUE": "explicit",
	}
	got := processHookEnvFromMap(source)
	if want := []string{"A_VALUE=explicit", "Z_EMPTY="}; !slices.Equal(got, want) {
		t.Fatalf("processHookEnvFromMap() = %v, want %v", got, want)
	}

	source["A_VALUE"] = "mutated"
	delete(source, "Z_EMPTY")
	if want := []string{"A_VALUE=explicit", "Z_EMPTY="}; !slices.Equal(got, want) {
		t.Fatalf("detached environment changed to %v, want %v", got, want)
	}
}

func TestP014HookClosedAndTrackedFenceEdges(t *testing.T) {
	t.Run("closed runtime rejects late mutation", func(t *testing.T) {
		var nilRuntime *hookRuntime
		nilRuntime.close(nil)

		originalErr := errors.New("original hook runtime error")
		runtime := &hookRuntime{
			initErr: originalErr,
			mounted: []hookRuntimeMount{{name: "original", mountID: 1}},
			closed:  true,
		}
		runtime.setInitErr(errors.New("late hook runtime error"))
		runtime.setMounted([]hookRuntimeMount{{name: "late", mountID: 2}})
		runtime.reset(nil)

		if got := runtime.getInitErr(); got != originalErr {
			t.Fatalf("closed runtime init error = %v, want original error", got)
		}
		runtime.mu.Lock()
		mounted := append([]hookRuntimeMount(nil), runtime.mounted...)
		runtime.mu.Unlock()
		if len(mounted) != 1 || mounted[0].name != "original" || mounted[0].mountID != 1 {
			t.Fatalf("closed runtime mounts changed to %+v", mounted)
		}
	})

	t.Run("nil owners retain no hook state", func(t *testing.T) {
		var nilLoop *AgentLoop
		if err := nilLoop.ensureHooksInitialized(context.Background()); err != nil {
			t.Fatalf("nil loop hook initialization error = %v", err)
		}
		if err := nilLoop.loadConfiguredHooks(context.Background(), &config.Config{}); err != nil {
			t.Fatalf("nil loop configured hook load error = %v", err)
		}

		var nilManager *HookManager
		observer, interceptor, approval := nilManager.timeoutSnapshot()
		if observer != 0 || interceptor != 0 || approval != 0 {
			t.Fatalf(
				"nil manager timeout snapshot = (%v, %v, %v), want all zero",
				observer,
				interceptor,
				approval,
			)
		}
		nilManager.unmountTracked("ignored", 1)
	})

	t.Run("tracked mount identity and close fence", func(t *testing.T) {
		manager := NewHookManager(nil)
		defer manager.Close()

		closed := make(chan struct{})
		mountID, err := manager.mountTracked(HookRegistration{
			Name:   "p014-tracked-fence",
			Source: HookSourceInProcess,
			Trust:  HookTrustTrusted,
			Hook:   &closeTrackingHook{closed: closed},
		})
		if err != nil {
			t.Fatalf("mountTracked() error = %v", err)
		}
		if mountID == 0 {
			t.Fatal("mountTracked() returned zero mount identity")
		}

		manager.unmountTracked("p014-tracked-fence", mountID+1)
		if hooks := manager.snapshotHooks(); len(hooks) != 1 {
			t.Fatalf("stale mount identity removed current hook: %+v", hooks)
		}
		select {
		case <-closed:
			t.Fatal("stale mount identity closed current hook")
		default:
		}

		manager.Close()
		select {
		case <-closed:
		default:
			t.Fatal("HookManager.Close() did not close tracked hook")
		}
		if err := manager.Mount(NamedHook("p014-after-close", struct{}{})); err == nil ||
			!strings.Contains(err.Error(), "closed") {
			t.Fatalf("post-close Mount() error = %v, want closed error", err)
		}
	})

	t.Run("manual process hook requires a current generation", func(t *testing.T) {
		loop := &AgentLoop{}
		err := loop.MountProcessHook(context.Background(), "p014-no-generation", ProcessHookOptions{
			Command:      []string{"must-not-start"},
			InterceptLLM: true,
		})
		if err == nil || !strings.Contains(err.Error(), "runtime generation is not configured") {
			t.Fatalf("MountProcessHook() error = %v, want missing generation error", err)
		}
	})
}

func TestP014ConfiguredHookLoadFailureCleanup(t *testing.T) {
	t.Run("stale generation fails before process launch", func(t *testing.T) {
		cfgA := newConfiguredHookConfig(t, config.HooksConfig{})
		cfgB := newConfiguredHookConfig(t, config.HooksConfig{
			Enabled: true,
			Processes: map[string]config.ProcessHookConfig{
				"p014-stale-process": {
					Enabled:   true,
					Command:   []string{"must-not-start"},
					Intercept: []string{"before_llm"},
				},
			},
		})
		manager := NewHookManager(nil)
		defer manager.Close()
		loop := &AgentLoop{
			cfg:             cfgA,
			hooks:           manager,
			executionPolicy: isolation.NewExecutionPolicy(cfgA.Isolation),
		}

		err := loop.loadConfiguredHooks(context.Background(), cfgB)
		if err == nil || !strings.Contains(err.Error(), "runtime generation is stale") {
			t.Fatalf("loadConfiguredHooks() error = %v, want stale generation error", err)
		}
		if hooks := manager.snapshotHooks(); len(hooks) != 0 {
			t.Fatalf("stale generation mounted hooks: %+v", hooks)
		}
	})

	t.Run("later factory failure rolls back and closes earlier hook", func(t *testing.T) {
		const (
			goodName = "p014-coverage-a-good"
			failName = "p014-coverage-b-fail"
		)
		closed := make(chan struct{})
		factoryErr := errors.New("p014 configured hook factory failure")
		if err := RegisterBuiltinHook(goodName, func(
			context.Context,
			config.BuiltinHookConfig,
		) (any, error) {
			return &closeTrackingHook{closed: closed}, nil
		}); err != nil {
			t.Fatalf("RegisterBuiltinHook(%q) error = %v", goodName, err)
		}
		t.Cleanup(func() { unregisterBuiltinHook(goodName) })
		if err := RegisterBuiltinHook(failName, func(
			context.Context,
			config.BuiltinHookConfig,
		) (any, error) {
			return nil, factoryErr
		}); err != nil {
			t.Fatalf("RegisterBuiltinHook(%q) error = %v", failName, err)
		}
		t.Cleanup(func() { unregisterBuiltinHook(failName) })

		manager := NewHookManager(nil)
		defer manager.Close()
		loop := &AgentLoop{hooks: manager}
		cfg := newConfiguredHookConfig(t, config.HooksConfig{
			Enabled: true,
			Builtins: map[string]config.BuiltinHookConfig{
				goodName: {Enabled: true},
				failName: {Enabled: true},
			},
		})

		err := loop.loadConfiguredHooks(context.Background(), cfg)
		if !errors.Is(err, factoryErr) {
			t.Fatalf("loadConfiguredHooks() error = %v, want %v", err, factoryErr)
		}
		if hooks := manager.snapshotHooks(); len(hooks) != 0 {
			t.Fatalf("failed configured load retained hooks: %+v", hooks)
		}
		select {
		case <-closed:
		default:
			t.Fatal("configured hook rollback did not close the earlier hook")
		}
	})
}

func TestAgentLoop_ProcessDirectWithChannel_InvalidConfiguredHookFails(t *testing.T) {
	provider := &llmHookTestProvider{}
	al := newConfiguredHookLoop(t, provider, config.HooksConfig{
		Enabled: true,
		Processes: map[string]config.ProcessHookConfig{
			"bad-hook": {
				Enabled:   true,
				Command:   processHookHelperCommand(),
				Intercept: []string{"not_supported"},
			},
		},
	})
	defer al.Close()

	_, err := al.ProcessDirectWithChannel(context.Background(), "hello", "session-1", "cli", "direct")
	if err == nil {
		t.Fatal("expected invalid configured hook error")
	}
}
