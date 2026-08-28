package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestRun_StartupFailuresReturnErrorAndEmitStructuredLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		prepare         func(t *testing.T, dir string) string
		wantErr         string
		forbiddenLogSub string
	}{
		{
			name: "invalid config returns load error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfgPath := filepath.Join(dir, "invalid-config.json")
				if err := os.WriteFile(cfgPath, []byte("{invalid-json"), 0o644); err != nil {
					t.Fatalf("WriteFile(invalid config) error = %v", err)
				}
				return cfgPath
			},
			wantErr:         "error loading config:",
			forbiddenLogSub: "error loading config:",
		},
		{
			name: "invalid config returns pre-check error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfg := config.DefaultConfig()
				cfg.Gateway.Port = 0
				cfgPath := filepath.Join(dir, "config.json")
				if err := config.SaveConfig(cfgPath, cfg); err != nil {
					t.Fatalf("SaveConfig() error = %v", err)
				}
				return cfgPath
			},
			wantErr:         "config pre-check failed: invalid gateway port: 0",
			forbiddenLogSub: "config pre-check failed: invalid gateway port: 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			configPath := tt.prepare(t, homeDir)

			cmd := exec.Command(os.Args[0], "-test.run=TestGatewayRunStartupFailureHelper")
			cmd.Env = append(os.Environ(),
				"GO_WANT_GATEWAY_RUN_HELPER=1",
				"PICO_TEST_HOME="+homeDir,
				"PICO_TEST_CONFIG="+configPath,
				"PICOCLAW_LOG_LEVEL=debug",
			)

			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper exited unexpectedly: %v\noutput:\n%s", err, string(output))
			}

			out := string(output)
			if !strings.Contains(out, tt.wantErr) {
				t.Fatalf("helper output missing expected error substring %q:\n%s", tt.wantErr, out)
			}

			logData, readErr := os.ReadFile(filepath.Join(homeDir, logPath, logFile))
			if readErr != nil {
				t.Fatalf("ReadFile(gateway.log) error = %v", readErr)
			}
			logText := string(logData)
			var startupRecord map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
				var record map[string]any
				if decodeErr := json.Unmarshal([]byte(line), &record); decodeErr != nil {
					continue
				}
				if record["message"] == "Gateway startup failed" {
					startupRecord = record
					break
				}
			}
			if startupRecord == nil {
				t.Fatalf("gateway.log missing structured startup failure record:\n%s", logText)
			}
			recordBytes, marshalErr := json.Marshal(startupRecord)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{tt.forbiddenLogSub, homeDir, configPath} {
				if strings.Contains(string(recordBytes), forbidden) {
					t.Fatalf("startup record contains private failure value %q: %s", forbidden, recordBytes)
				}
			}
			if startupRecord["error_class"] != "internal" ||
				startupRecord["error_digest"] == nil ||
				startupRecord["config_path_digest"] == nil ||
				startupRecord["home_path_digest"] == nil ||
				startupRecord["allow_empty"] != false ||
				startupRecord["debug_enabled"] != false {
				t.Fatalf("startup record lacks required safe fields: %#v", startupRecord)
			}
		})
	}
}

func TestStartupRuntimeBarrierPreservesOverdueCronUntilReadiness(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.Cron.Enabled = false
	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		msgBus,
		&startupBlockedProvider{reason: "not used"},
		agent.WithRuntimeStartupBarrier(),
	)
	defer func() {
		agentLoop.Stop()
		msgBus.Close()
		agentLoop.Close()
	}()

	executionPolicy, err := agentLoop.ExecutionPolicyForGeneration(cfg)
	if err != nil {
		t.Fatalf("ExecutionPolicyForGeneration() error = %v", err)
	}
	cronService, err := setupCronTool(
		agentLoop,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		time.Minute,
		cfg,
		executionPolicy,
	)
	if err != nil {
		t.Fatalf("setupCronTool() error = %v", err)
	}
	defer cronService.Stop()

	jobRan := make(chan struct{}, 1)
	cronService.SetOnJobContext(func(context.Context, *cron.CronJob) (string, error) {
		jobRan <- struct{}{}
		return "ok", nil
	})
	runAt := time.Now().Add(25 * time.Millisecond).UnixMilli()
	job, err := cronService.AddJob(
		"startup-overdue",
		cron.CronSchedule{Kind: "at", AtMS: &runAt},
		"run only after ready",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	if err = cronService.Start(); err != nil {
		t.Fatalf("CronService.Start() error = %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	select {
	case <-jobRan:
		t.Fatal("overdue cron ran before startup readiness released the runtime")
	default:
	}
	pending, ok := cronService.GetJob(job.ID)
	if !ok || pending.State.NextRunAtMS == nil {
		t.Fatalf("overdue cron was consumed before readiness: %#v", pending)
	}

	// Models the gateway's SetReady/publish-ready boundary.
	agentLoop.ReleaseRuntimeStartupBarrier()
	select {
	case <-jobRan:
	case <-time.After(2 * time.Second):
		t.Fatal("overdue cron did not run after startup readiness")
	}
}

func TestSetupCronToolUsesExactGenerationExecutionPolicy(t *testing.T) {
	const environmentName = "PICOCLAW_GATEWAY_CRON_POLICY"

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.RestrictToWorkspace = false
	cfg.Isolation.EnvironmentAllowlist = []string{"PATH", environmentName}

	t.Setenv(environmentName, "generation-a")
	executionPolicy := isolation.NewExecutionPolicy(cfg.Isolation)
	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoopWithExecutionPolicy(
		cfg,
		msgBus,
		&startupBlockedProvider{reason: "not used"},
		executionPolicy,
	)
	defer func() {
		agentLoop.Close()
		msgBus.Close()
	}()

	// A compatibility constructor here would recapture this changed value.
	// Gateway Cron must instead retain the policy installed on generation A.
	t.Setenv(environmentName, "ambient-after-generation-a")
	cronService, err := setupCronTool(
		agentLoop,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		time.Minute,
		cfg,
		executionPolicy,
	)
	if err != nil {
		t.Fatalf("setupCronTool() error = %v", err)
	}
	defer cronService.Stop()

	defaultAgent := agentLoop.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("default agent is nil")
	}
	registered, ok := defaultAgent.Tools.GetRegistered("cron")
	if !ok {
		t.Fatal("Cron tool was not registered")
	}
	cronTool, ok := registered.(*tools.CronTool)
	if !ok {
		t.Fatalf("registered Cron tool type = %T", registered)
	}

	result := cronTool.ExecuteJob(context.Background(), &cron.CronJob{
		Payload: cron.CronPayload{
			Command: gatewayPolicyEnvironmentCommand(environmentName),
			Channel: "cli",
			To:      "direct",
		},
	})
	if result != "ok" {
		t.Fatalf("ExecuteJob() = %q, want ok", result)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if !strings.Contains(outbound.Content, "generation-a") {
			t.Fatalf("Cron output = %q, want generation-a", outbound.Content)
		}
		if strings.Contains(outbound.Content, "ambient-after-generation-a") {
			t.Fatalf("Cron output crossed generation policy: %q", outbound.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cron command did not publish its result")
	}
}

func gatewayPolicyEnvironmentCommand(name string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`[Console]::Out.Write($env:%s)`, name)
	}
	return fmt.Sprintf(`printf '%%s' "$%s"`, name)
}

func TestHandleConfigReloadRollbackRestoresExactExecutionPolicy(t *testing.T) {
	const environmentName = "PICOCLAW_GATEWAY_ROLLBACK_POLICY"

	oldCfg := config.DefaultConfig()
	oldCfg.Agents.Defaults.Workspace = t.TempDir()
	oldCfg.Isolation.EnvironmentAllowlist = []string{"PATH", environmentName}
	newCfg := config.DefaultConfig()
	newCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg.Isolation.EnvironmentAllowlist = []string{"PATH", environmentName}

	t.Setenv(environmentName, "generation-a")
	oldExecutionPolicy := isolation.NewExecutionPolicy(oldCfg.Isolation)
	msgBus := bus.NewMessageBus()
	oldProvider := &startupBlockedProvider{reason: "not used"}
	agentLoop := agent.NewAgentLoopWithExecutionPolicy(
		oldCfg,
		msgBus,
		oldProvider,
		oldExecutionPolicy,
	)
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{HealthServer: healthServer}
	defer func() {
		agentLoop.Close()
		msgBus.Close()
	}()

	t.Setenv(environmentName, "generation-b")
	forcedRestartErr := errors.New("forced candidate service restart failure")
	restartCalls := 0
	serviceOps := configReloadServiceOps{
		stop: func(*services, time.Duration, bool) error { return nil },
		restart: func(
			_ context.Context,
			currentLoop *agent.AgentLoop,
			_ *services,
			_ *bus.MessageBus,
		) error {
			restartCalls++
			currentCfg := currentLoop.GetConfig()
			currentPolicy, policyErr := currentLoop.ExecutionPolicyForGeneration(currentCfg)
			if policyErr != nil {
				return policyErr
			}
			captured, ok := currentPolicy.LookupEnvironment(environmentName)
			if !ok {
				return fmt.Errorf("%s was not captured", environmentName)
			}
			if currentCfg == newCfg {
				if captured != "generation-b" {
					return fmt.Errorf("candidate policy = %q, want generation-b", captured)
				}
				// A reconstructed rollback policy would capture this later value.
				t.Setenv(environmentName, "ambient-after-generation-b")
				return forcedRestartErr
			}
			if currentCfg != oldCfg {
				return fmt.Errorf("unexpected recovered config identity")
			}
			if captured != "generation-a" {
				return fmt.Errorf("rollback policy = %q, want generation-a", captured)
			}
			return nil
		},
	}
	var providerRef providers.LLMProvider = oldProvider

	err := handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, forcedRestartErr) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v, want %v", err, forcedRestartErr)
	}
	if restartCalls != 2 {
		t.Fatalf("restart calls = %d, want candidate and rollback", restartCalls)
	}
	if agentLoop.GetConfig() != oldCfg {
		t.Fatal("rollback did not restore config A")
	}
	if providerRef != oldProvider {
		t.Fatal("rollback did not retain provider A")
	}
	restoredPolicy, policyErr := agentLoop.ExecutionPolicyForGeneration(oldCfg)
	if policyErr != nil {
		t.Fatalf("ExecutionPolicyForGeneration(old) error = %v", policyErr)
	}
	if captured, ok := restoredPolicy.LookupEnvironment(environmentName); !ok || captured != "generation-a" {
		t.Fatalf("restored policy value = %q, %v; want generation-a, true", captured, ok)
	}
}

func TestHandleConfigReloadRejectsInvalidExecutionPolicyBeforeDrain(t *testing.T) {
	oldCfg := config.DefaultConfig()
	oldCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg := config.DefaultConfig()
	newCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg.Isolation.EnvironmentAllowlist = []string{"PATH", "path"}

	msgBus := bus.NewMessageBus()
	oldProvider := &startupBlockedProvider{reason: "not used"}
	agentLoop := agent.NewAgentLoopWithExecutionPolicy(
		oldCfg,
		msgBus,
		oldProvider,
		isolation.NewExecutionPolicy(oldCfg.Isolation),
	)
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{HealthServer: healthServer}
	defer func() {
		agentLoop.Close()
		msgBus.Close()
	}()

	stopCalls := 0
	restartCalls := 0
	serviceOps := configReloadServiceOps{
		stop: func(*services, time.Duration, bool) error {
			stopCalls++
			return nil
		},
		restart: func(
			context.Context,
			*agent.AgentLoop,
			*services,
			*bus.MessageBus,
		) error {
			restartCalls++
			return nil
		},
	}
	var providerRef providers.LLMProvider = oldProvider

	err := handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"invalid replacement subprocess execution policy",
	) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v", err)
	}
	if stopCalls != 0 || restartCalls != 0 {
		t.Fatalf("service calls = stop %d, restart %d; want 0, 0", stopCalls, restartCalls)
	}
	if agentLoop.GetConfig() != oldCfg {
		t.Fatal("invalid policy replaced config generation")
	}
	if providerRef != oldProvider {
		t.Fatal("invalid policy replaced provider generation")
	}
	if !healthServer.IsReady() {
		t.Fatal("invalid policy changed readiness before admission drain")
	}
	if _, policyErr := agentLoop.ExecutionPolicyForGeneration(oldCfg); policyErr != nil {
		t.Fatalf("generation A policy changed: %v", policyErr)
	}
}

func TestGatewayRunStartupFailureHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GATEWAY_RUN_HELPER") != "1" {
		return
	}

	homeDir := os.Getenv("PICO_TEST_HOME")
	configPath := os.Getenv("PICO_TEST_CONFIG")

	err := Run(false, homeDir, configPath, false)
	if err == nil {
		fmt.Fprintln(os.Stdout, "expected startup error, got nil")
		os.Exit(2)
	}

	fmt.Fprintln(os.Stdout, err.Error())
	os.Exit(0)
}

func TestLogChannelVoiceCapabilitiesHandlesNilManager(t *testing.T) {
	t.Parallel()

	logChannelVoiceCapabilities(nil, true, true)
}

func TestStartupBlockedProviderReportsReason(t *testing.T) {
	t.Parallel()

	provider := &startupBlockedProvider{reason: "startup blocked"}
	resp, err := provider.Chat(context.Background(), nil, nil, "", nil)
	if err == nil || err.Error() != "startup blocked" {
		t.Fatalf("Chat() error = %v, want startup blocked", err)
	}
	if resp != nil {
		t.Fatalf("Chat() response = %#v, want nil", resp)
	}
}

func TestCollectGatewayStartupStatusHandlesMalformedInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		startupInfo         map[string]any
		wantToolsCount      int
		wantSkillsAvailable int
		wantSkillsTotal     int
	}{
		{
			name:        "missing info",
			startupInfo: map[string]any{},
		},
		{
			name: "wrong map shapes",
			startupInfo: map[string]any{
				"tools":  "unexpected",
				"skills": []any{"unexpected"},
			},
		},
		{
			name: "valid startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": 3,
				},
				"skills": map[string]any{
					"available": 2,
					"total":     5,
				},
			},
			wantToolsCount:      3,
			wantSkillsAvailable: 2,
			wantSkillsTotal:     5,
		},
		{
			name: "json number startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": float64(4),
				},
				"skills": map[string]any{
					"available": float64(1),
					"total":     float64(6),
				},
			},
			wantToolsCount:      4,
			wantSkillsAvailable: 1,
			wantSkillsTotal:     6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := collectGatewayStartupStatus(tt.startupInfo)
			if got.toolsCount != tt.wantToolsCount {
				t.Fatalf("toolsCount = %d, want %d", got.toolsCount, tt.wantToolsCount)
			}
			if got.skillsAvailable != tt.wantSkillsAvailable {
				t.Fatalf("skillsAvailable = %d, want %d", got.skillsAvailable, tt.wantSkillsAvailable)
			}
			if got.skillsTotal != tt.wantSkillsTotal {
				t.Fatalf("skillsTotal = %d, want %d", got.skillsTotal, tt.wantSkillsTotal)
			}
		})
	}
}

func TestPublishGatewayEvent(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	t.Cleanup(func() {
		if err := eventBus.Close(); err != nil {
			t.Fatalf("Close runtime event bus: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindGatewayStart).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "gateway-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sub.Close(); err != nil {
			t.Fatalf("Close subscription: %v", err)
		}
	})

	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		bus.NewMessageBus(),
		&startupBlockedProvider{reason: "not used"},
		agent.WithRuntimeEvents(eventBus),
	)
	t.Cleanup(al.Close)

	startedAt := time.Now().Add(-1500 * time.Millisecond)
	publishGatewayEvent(al, runtimeevents.KindGatewayStart, startedAt, nil)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindGatewayStart ||
		evt.Source.Component != "gateway" ||
		evt.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("gateway event = %+v", evt)
	}
	payload, ok := evt.Payload.(gatewayEventPayload)
	if !ok {
		t.Fatalf("payload type = %T, want gatewayEventPayload", evt.Payload)
	}
	if payload.DurationMS <= 0 {
		t.Fatalf("DurationMS = %d, want positive", payload.DurationMS)
	}
	if evt.Attrs["duration_ms"] == nil {
		t.Fatalf("gateway event attrs missing duration_ms: %#v", evt.Attrs)
	}
}

func TestShutdownGatewayClosesMessageBus(t *testing.T) {
	msgBus := bus.NewMessageBus()
	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		msgBus,
		&startupBlockedProvider{reason: "not used"},
	)
	msgBus.SetEventPublisher(al.RuntimeEventBus())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, eventsCh, err := al.RuntimeEventBus().Channel().OfKind(runtimeevents.KindBusCloseCompleted).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "bus-close-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	defer func() {
		_ = sub.Close()
	}()

	shutdownGateway(&services{}, al, &startupBlockedProvider{reason: "not used"}, msgBus, true)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindBusCloseCompleted {
		t.Fatalf("shutdown event kind = %q, want %q", evt.Kind, runtimeevents.KindBusCloseCompleted)
	}
	if err := msgBus.PublishVoiceControl(context.Background(), bus.VoiceControl{}); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("PublishVoiceControl after shutdown error = %v, want %v", err, bus.ErrBusClosed)
	}
}

func receiveGatewayRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway runtime event")
		return runtimeevents.Event{}
	}
}
