package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type p015B2CReloadCountingError struct {
	calls  atomic.Int32
	canary string
}

func (err *p015B2CReloadCountingError) Error() string {
	err.calls.Add(1)
	return err.canary
}

func TestP015B2CReloadRollbackSealsFailureAndPreservesGeneration(t *testing.T) {
	oldCfg := config.DefaultConfig()
	oldCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg := config.DefaultConfig()
	newCfg.Agents.Defaults.Workspace = t.TempDir()

	msgBus := bus.NewMessageBus()
	oldProvider := &startupBlockedProvider{reason: "old generation"}
	agentLoop := agent.NewAgentLoopWithExecutionPolicy(
		oldCfg,
		msgBus,
		oldProvider,
		isolation.NewExecutionPolicy(oldCfg.Isolation),
	)
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{HealthServer: healthServer}
	t.Cleanup(func() {
		agentLoop.Close()
		msgBus.Close()
	})

	forced := &p015B2CReloadCountingError{
		canary: "P015_B2C_RELOAD_RESTART_ERROR_RAW_CANARY",
	}
	stopCalls, restartCalls := 0, 0
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
			if restartCalls == 1 {
				return forced
			}
			return nil
		},
	}
	var providerRef providers.LLMProvider = oldProvider
	var reloadErr error
	records, raw := captureGatewaySafeRecords(t, func() {
		_ = p015B2CCaptureStdout(t, func() {
			reloadErr = handleConfigReloadWithServiceOps(
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
		})
	})
	if !errors.Is(reloadErr, forced) {
		t.Fatal("reload did not retain the exact candidate restart failure")
	}
	if stopCalls != 2 || restartCalls != 2 {
		t.Fatalf(
			"reload rollback service calls = stop %d, restart %d; want 2, 2",
			stopCalls,
			restartCalls,
		)
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider || !healthServer.IsReady() {
		t.Fatal("reload failure did not restore the exact old generation and readiness")
	}
	if got := forced.calls.Load(); got != 1 {
		t.Fatalf("restart error Error() calls = %d, want only the functional wrap call", got)
	}
	record := p015B2CReloadRecord(t, records, "  ⚠ Error restarting services")
	if record["component"] != "gateway" || record["error_class"] != "internal" {
		t.Fatalf("reload restart failure projection = %#v", record)
	}
	if strings.Contains(raw, forced.canary) {
		t.Fatalf("reload failure leaked raw error text: %s", raw)
	}
}

func TestP015B2CReloadSuccessPublishesGenerationAndClosedLogLevel(t *testing.T) {
	oldCfg := config.DefaultConfig()
	oldCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg := config.DefaultConfig()
	newCfg.Agents.Defaults.Workspace = t.TempDir()
	newCfg.Gateway.LogLevel = "debug"

	msgBus := bus.NewMessageBus()
	oldProvider := &startupBlockedProvider{reason: "old generation"}
	agentLoop := agent.NewAgentLoopWithExecutionPolicy(
		oldCfg,
		msgBus,
		oldProvider,
		isolation.NewExecutionPolicy(oldCfg.Isolation),
	)
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{HealthServer: healthServer}
	t.Cleanup(func() {
		agentLoop.Close()
		msgBus.Close()
	})

	stopCalls, restartCalls := 0, 0
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
	var reloadErr error
	records, raw := captureGatewaySafeRecords(t, func() {
		_ = p015B2CCaptureStdout(t, func() {
			reloadErr = handleConfigReloadWithServiceOps(
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
		})
	})
	if reloadErr != nil {
		t.Fatalf("successful reload error = %v", reloadErr)
	}
	if stopCalls != 1 || restartCalls != 1 {
		t.Fatalf("successful reload service calls = stop %d, restart %d; want 1, 1", stopCalls, restartCalls)
	}
	if agentLoop.GetConfig() != newCfg || providerRef == oldProvider || !healthServer.IsReady() {
		t.Fatal("successful reload did not publish the exact new generation")
	}
	record := p015B2CReloadRecord(t, records, "Log level changing from current")
	if record["component"] != "logger" || record["log_level"] != "debug" {
		t.Fatalf("reload log-level projection = %#v; raw=%s", record, raw)
	}
}

func TestP015B2CRestartServicesPreservesConsoleCountsAndVoiceDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Heartbeat.Enabled = false
	cfg.Tools.Cron.Enabled = false
	cfg.Tools.MCP.Enabled = false
	cfg.Devices.Enabled = true
	cfg.Devices.MonitorUSB = false

	msgBus := bus.NewMessageBus()
	provider := &startupBlockedProvider{reason: "unused"}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	channelManager, err := channels.NewManager(cfg, msgBus, nil)
	if err != nil {
		t.Fatalf("channels.NewManager() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	const authToken = "p015-b2c-reload-runtime-token"
	healthServer := health.NewServer("127.0.0.1", 0, authToken)
	channelManager.SetupHTTPServerListeners(
		[]net.Listener{listener}, listener.Addr().String(), healthServer,
	)
	runningServices := &services{
		ChannelManager: channelManager,
		HealthServer:   healthServer,
		authToken:      authToken,
	}
	var restartErr error
	var console string
	records, raw := captureGatewaySafeRecords(t, func() {
		console = p015B2CCaptureStdout(t, func() {
			restartErr = restartServices(
				context.Background(), agentLoop, runningServices, msgBus,
			)
		})
	})
	if cleanupErr := stopAndCleanupServices(runningServices, 2*time.Second, false); cleanupErr != nil {
		t.Fatalf("stopAndCleanupServices() error = %v", cleanupErr)
	}
	_ = listener.Close()
	agentLoop.Close()
	msgBus.Close()
	if restartErr != nil {
		t.Fatalf("restartServices() error = %v", restartErr)
	}

	wantConsole := []string{
		"  ✓ Heartbeat service restarted\n",
		"  ✓ Channels restarted.\n",
		"  ⚠ Warning: No channels enabled\n",
		"  ✓ Device event service restarted\n",
		"  ✓ Cron service restarted\n",
	}
	for _, line := range wantConsole {
		if strings.Count(console, line) != 1 {
			t.Errorf("restart console occurrence for %q != 1: %q", line, console)
		}
	}
	if got := strings.Count(console, "\n"); got != len(wantConsole) {
		t.Errorf("restart console records = %d, want %d: %q", got, len(wantConsole), console)
	}
	record := p015B2CReloadRecord(t, records, "Transcription disabled")
	if record["component"] != "voice" {
		t.Fatalf("reload voice-disabled projection = %#v; raw=%s", record, raw)
	}
}

func TestP015B2CConfigWatcherFailureKeepsPreviousConfigAndSealsError(t *testing.T) {
	const canary = "P015_B2C_CONFIG_WATCHER_RAW_CANARY"
	path := filepath.Join(t.TempDir(), canary+".json")
	if err := config.SaveConfig(path, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	ready := make(chan struct{})
	processed := make(chan struct{}, 1)
	var configChan chan *config.Config
	records, _ := captureGatewaySafeRecords(t, func() {
		var stop func()
		configChan, stop = setupConfigWatcherPolling(
			path,
			true,
			configWatcherPollingTiming{
				pollInterval: 5 * time.Millisecond,
				settleDelay:  0,
				ready:        ready,
				processed:    processed,
			},
		)
		defer stop()
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("config watcher did not publish its initial snapshot readiness")
		}
		if err := os.WriteFile(path, []byte("{"+canary), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		select {
		case <-processed:
		case <-time.After(2 * time.Second):
			t.Fatal("config watcher did not process the changed file")
		}
	})
	select {
	case candidate := <-configChan:
		t.Fatalf("invalid watched config was published: %#v", candidate)
	default:
	}
	for _, message := range []string{
		"🔍 Config file change detected",
		"⚠ Error loading new config",
		"  Using previous valid config",
	} {
		p015B2CReloadRecord(t, records, message)
	}
	errorRecord := p015B2CReloadRecord(t, records, "⚠ Error loading new config")
	if errorRecord["error_class"] != "internal" {
		t.Fatalf("config watcher failure projection = %#v", errorRecord)
	}
	encoded, err := json.Marshal(errorRecord)
	if err != nil {
		t.Fatalf("Marshal(errorRecord) error = %v", err)
	}
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), path) {
		t.Fatalf("Gateway config watcher record leaked raw path/content: %s", encoded)
	}
}

func p015B2CReloadRecord(
	t *testing.T,
	records []map[string]any,
	message string,
) map[string]any {
	t.Helper()
	var found map[string]any
	for _, record := range records {
		if record["message"] != message {
			continue
		}
		if found != nil {
			t.Fatalf("reload record %q appeared more than once: %#v", message, records)
		}
		found = record
	}
	if found == nil {
		t.Fatalf("reload record %q missing: %#v", message, records)
	}
	return found
}

var p015B2CStdoutCaptureMu sync.Mutex

func TestP015B2CStdoutCaptureRestoresAfterGoexit(t *testing.T) {
	original := os.Stdout
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p015B2CCaptureStdout(t, func() {
			runtime.Goexit()
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stdout capture did not release process-global state after Goexit")
	}
	if os.Stdout != original {
		t.Fatal("stdout capture did not restore the original writer after Goexit")
	}

	const canary = "p015b2c-stdout-after-goexit"
	got := p015B2CCaptureStdout(t, func() {
		_, _ = os.Stdout.Write([]byte(canary))
	})
	if got != canary {
		t.Fatalf("stdout capture after Goexit = %q, want %q", got, canary)
	}
}

func p015B2CCaptureStdout(t *testing.T, emit func()) string {
	t.Helper()
	p015B2CStdoutCaptureMu.Lock()
	defer p015B2CStdoutCaptureMu.Unlock()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	emit()
	os.Stdout = original
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data)
}
