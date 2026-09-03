package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/pid"
)

func TestRunContextRejectsNilContext(t *testing.T) {
	err := RunContext(nil, RunOptions{})
	if err == nil {
		t.Fatal("RunContext(nil) error = nil, want rejection")
	}
	if got, want := err.Error(), "gateway runtime context is required"; got != want {
		t.Fatalf("RunContext(nil) error = %q, want %q", got, want)
	}
}

func TestRunContextCanceledBeforeStartupDoesNotLoadConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RunContext(ctx, RunOptions{
		ConfigPath: t.TempDir() + "/missing.json",
	})
	if err != nil {
		t.Fatalf("RunContext(canceled) error = %v, want nil", err)
	}
}

func TestRunRejectsNilConfiguredContext(t *testing.T) {
	err := Run(false, "", "", true, func(configuration *runConfiguration) {
		configuration.context = nil
		configuration.standalone = false
	})
	if err == nil || err.Error() != "gateway runtime context is required" {
		t.Fatalf("Run(nil configured context) error = %v, want context rejection", err)
	}
}

func TestRunReportsStandalonePanicLogInitializationFailure(t *testing.T) {
	homePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homePath, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("WriteFile(homePath) error = %v", err)
	}

	err := Run(false, homePath, filepath.Join(homePath, "missing.json"), true)
	if err == nil || !strings.Contains(err.Error(), "error initializing panic log") {
		t.Fatalf("Run(unusable panic path) error = %v, want panic log initialization failure", err)
	}
}

func TestRunReportsStandaloneFileLogFailureWithoutTerminatingProcess(t *testing.T) {
	homePath := t.TempDir()
	logFilePath := filepath.Join(homePath, logPath, logFile)
	if err := os.MkdirAll(logFilePath, 0o700); err != nil {
		t.Fatalf("MkdirAll(gateway.log) error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Port = 0
	configPath := filepath.Join(homePath, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	originalFatalExit := zerolog.FatalExitFunc
	fatalExitCalls := 0
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }
	t.Cleanup(func() { zerolog.FatalExitFunc = originalFatalExit })

	err := Run(false, homePath, configPath, true)
	if err == nil || !strings.Contains(err.Error(), "config pre-check failed") {
		t.Fatalf("Run(after file log failure) error = %v, want config pre-check failure", err)
	}
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
}

func TestApplyGatewayHostOverride(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		applyGatewayHostOverride(nil, "127.0.0.1")
	})

	tests := []struct {
		name     string
		value    string
		wantHost string
	}{
		{name: "empty keeps configured host", value: "", wantHost: "configured.example"},
		{name: "whitespace keeps configured host", value: " \t\n", wantHost: "configured.example"},
		{name: "override is trimmed", value: "  127.0.0.1 \t", wantHost: "127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Gateway: config.GatewayConfig{Host: "configured.example"}}

			applyGatewayHostOverride(cfg, test.value)

			if got := cfg.Gateway.Host; got != test.wantHost {
				t.Fatalf("gateway host = %q, want %q (override %q)", got, test.wantHost, strings.TrimSpace(test.value))
			}
		})
	}
}

func TestEmbeddedGatewayReplacementSafety(t *testing.T) {
	if embeddedGatewayReplacementUnsafe(nil) ||
		embeddedGatewayReplacementUnsafe([]string{config.ChannelPico}) {
		t.Fatal("empty/Pico-only channel runtime was marked replacement-unsafe")
	}
	if !embeddedGatewayReplacementUnsafe([]string{config.ChannelPico, config.ChannelSlack}) {
		t.Fatal("non-Pico channel runtime was marked replacement-safe")
	}
}

func TestRunStandaloneOwnershipShutsDownFromConfiguredContext(t *testing.T) {
	home, configPath, port := newGatewayRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan pid.PidFileData, 1)

	err := Run(false, home, configPath, true, func(configuration *runConfiguration) {
		configuration.context = ctx
		configuration.onReady = func(data pid.PidFileData) {
			ready <- data
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("Run(standalone ownership) error = %v", err)
	}
	select {
	case data := <-ready:
		if data.PID != os.Getpid() || data.Port != port || data.Token == "" {
			t.Fatalf("ready PID data = %#v, want current process on port %d", data, port)
		}
	default:
		t.Fatal("standalone runtime did not report readiness")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".picoclaw.pid")); !os.IsNotExist(statErr) {
		t.Fatalf("PID metadata survived standalone shutdown: %v", statErr)
	}
}

func TestRunContextRecoversReadyCallbackPanicAndRetiresRuntime(t *testing.T) {
	home, configPath, _ := newGatewayRuntimeFixture(t)
	err := RunContext(context.Background(), RunOptions{
		HomePath:          home,
		ConfigPath:        configPath,
		AllowEmptyStartup: true,
		OnReady: func(pid.PidFileData) {
			panic("ready callback panic")
		},
	})
	if !errors.Is(err, ErrRuntimeNotRetired) ||
		!strings.Contains(err.Error(), "gateway runtime panicked") {
		t.Fatalf("RunContext(panicking OnReady) error = %v, want retirement-required panic", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".picoclaw.pid")); !os.IsNotExist(statErr) {
		t.Fatalf("PID metadata survived panic cleanup: %v", statErr)
	}
}

func TestRunContextDispatchesManualReload(t *testing.T) {
	home, configPath, port := newGatewayRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan pid.PidFileData, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunContext(ctx, RunOptions{
			HomePath:            home,
			ConfigPath:          configPath,
			AllowEmptyStartup:   true,
			GatewayHostOverride: "127.0.0.1",
			OnReady: func(data pid.PidFileData) {
				ready <- data
			},
		})
	}()

	var data pid.PidFileData
	select {
	case data = <-ready:
	case runErr := <-done:
		t.Fatalf("RunContext() exited before ready: %v", runErr)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for embedded gateway readiness")
	}

	if status, requestErr := postGatewayReload(port, data.Token); requestErr != nil {
		cancel()
		t.Fatalf("POST /reload error = %v", requestErr)
	} else if status != http.StatusOK {
		cancel()
		t.Fatalf("POST /reload status = %d, want %d", status, http.StatusOK)
	}

	// The reload callback sets the generation's reloading flag before it queues
	// work. A later 200 response therefore proves the first reload ran through
	// executeReload and cleared that flag; 500 is expected while it is active.
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, requestErr := postGatewayReload(port, data.Token)
		if requestErr != nil {
			cancel()
			t.Fatalf("poll POST /reload error = %v", requestErr)
		}
		if status == http.StatusOK {
			break
		}
		if status != http.StatusInternalServerError {
			cancel()
			t.Fatalf("poll POST /reload status = %d, want 200 or 500", status)
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("timed out waiting for manual reload completion")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunContext() shutdown after reload error = %v", runErr)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("timed out waiting for embedded gateway shutdown after reload")
	}
}

func TestSetupAndStartServicesHonorsLifecycleCancellationGuards(t *testing.T) {
	t.Run("before heartbeat start", func(t *testing.T) {
		_, configPath, _ := newGatewayRuntimeFixture(t)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		setupErr := runGatewayServiceSetup(t, cfg, ctx)
		if !errors.Is(setupErr, context.Canceled) {
			t.Fatalf("setup error = %v, want cancellation before heartbeat", setupErr)
		}
	})

	t.Run("between heartbeat and cron start", func(t *testing.T) {
		_, configPath, _ := newGatewayRuntimeFixture(t)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		lifecycle := &cancelOnNthErrContext{
			Context:      context.Background(),
			cancelOnCall: 2,
		}

		setupErr := runGatewayServiceSetup(t, cfg, lifecycle)
		if !errors.Is(setupErr, context.Canceled) {
			t.Fatalf("setup error = %v, want cancellation before cron", setupErr)
		}
		if lifecycle.errCalls != 2 {
			t.Fatalf("lifecycle Err calls = %d, want 2", lifecycle.errCalls)
		}
	})
}

func TestSetupAndStartServicesRejectsCorruptEventStorage(t *testing.T) {
	home, _, port := newGatewayRuntimeFixture(t)
	databasePath := filepath.Join(home, "eventing", "events.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll(event database) error = %v", err)
	}
	if err := os.WriteFile(databasePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("WriteFile(event database) error = %v", err)
	}
	cfg := eventAutomationTestConfig(
		filepath.Join(home, "workspace"),
		databasePath,
		true,
		false,
	)
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = port

	err := runGatewayServiceSetup(t, cfg, context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate event automation storage") {
		t.Fatalf("setup error = %v, want corrupt event storage rejection", err)
	}
}

func TestRunContextStartsAndStopsReusableInProcessRuntime(t *testing.T) {
	const picoToken = "embedded-runtime-test-token"
	home := t.TempDir()
	t.Setenv("PICOCLAW_HOME", home)
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve gateway port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatalf("close reserved gateway port: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = port
	cfg.Gateway.HotReload = false
	picoChannel := cfg.Channels.Get(config.ChannelPico)
	if picoChannel == nil {
		t.Fatal("DefaultConfig() has no Pico channel")
	}
	picoSettings, err := picoChannel.GetDecoded()
	if err != nil {
		t.Fatalf("decode Pico settings: %v", err)
	}
	picoChannel.Enabled = true
	picoSettings.(*config.PicoSettings).SetToken(picoToken)
	configPath := filepath.Join(home, "config.json")
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	startAndStop := func() pid.PidFileData {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan pid.PidFileData, 1)
		done := make(chan error, 1)
		go func() {
			done <- RunContext(ctx, RunOptions{
				HomePath:            home,
				ConfigPath:          configPath,
				AllowEmptyStartup:   true,
				GatewayHostOverride: "127.0.0.1",
				OnReady: func(data pid.PidFileData) {
					ready <- data
				},
			})
		}()

		var data pid.PidFileData
		select {
		case data = <-ready:
		case runErr := <-done:
			t.Fatalf("RunContext() exited before ready: %v", runErr)
		case <-time.After(15 * time.Second):
			t.Fatal("timed out waiting for embedded gateway readiness")
		}
		if data.PID != os.Getpid() || data.Token == "" || data.Port != port {
			t.Fatalf("ready PID data = %#v, want current PID, token, port %d", data, port)
		}

		request, requestErr := http.NewRequest(
			http.MethodGet,
			fmt.Sprintf("http://127.0.0.1:%s/health", strconv.Itoa(port)),
			nil,
		)
		if requestErr != nil {
			t.Fatalf("NewRequest() error = %v", requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+data.Token)
		response, requestErr := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if requestErr != nil {
			t.Fatalf("embedded health request error = %v", requestErr)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("embedded health status = %d, want %d", response.StatusCode, http.StatusOK)
		}

		dialer := websocket.Dialer{Subprotocols: []string{"token." + picoToken}}
		headers := http.Header{"Origin": []string{"http://127.0.0.1"}}
		connection, websocketResponse, dialErr := dialer.Dial(
			fmt.Sprintf("ws://127.0.0.1:%d/pico/ws", port),
			headers,
		)
		if websocketResponse != nil && websocketResponse.Body != nil {
			_ = websocketResponse.Body.Close()
		}
		if dialErr != nil {
			t.Fatalf("embedded Pico websocket dial error = %v", dialErr)
		}
		_ = connection.Close()

		cancel()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Fatalf("RunContext() shutdown error = %v", runErr)
			}
		case <-time.After(40 * time.Second):
			t.Fatal("timed out waiting for embedded gateway shutdown")
		}
		if _, statErr := os.Stat(filepath.Join(home, ".picoclaw.pid")); !os.IsNotExist(statErr) {
			t.Fatalf("PID metadata survived clean shutdown: %v", statErr)
		}
		return data
	}

	first := startAndStop()
	second := startAndStop()
	if first.PID != second.PID || first.Token == second.Token {
		t.Fatalf(
			"runtime identities = (%d, %q), (%d, %q); want same PID and rotated token",
			first.PID,
			first.Token,
			second.PID,
			second.Token,
		)
	}
}

func newGatewayRuntimeFixture(t *testing.T) (home, configPath string, port int) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("PICOCLAW_HOME", home)
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve gateway port: %v", err)
	}
	port = listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatalf("close reserved gateway port: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = port
	cfg.Gateway.HotReload = false
	configPath = filepath.Join(home, "config.json")
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return home, configPath, port
}

func postGatewayReload(port int, token string) (int, error) {
	request, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/reload", port),
		nil,
	)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

type cancelOnNthErrContext struct {
	context.Context
	errCalls     int
	cancelOnCall int
}

func (ctx *cancelOnNthErrContext) Err() error {
	ctx.errCalls++
	if ctx.errCalls >= ctx.cancelOnCall {
		return context.Canceled
	}
	return nil
}

func runGatewayServiceSetup(
	t *testing.T,
	cfg *config.Config,
	lifecycleCtx context.Context,
) error {
	t.Helper()
	_, listenResult, err := openGatewayListeners(cfg.Gateway.Host, cfg.Gateway.Port)
	if err != nil {
		t.Fatalf("openGatewayListeners() error = %v", err)
	}
	defer func() {
		for _, listener := range listenResult.Listeners {
			_ = listener.Close()
		}
	}()

	messageBus := bus.NewMessageBus()
	provider := &startupBlockedProvider{reason: "not used"}
	agentLoop := agent.NewAgentLoop(cfg, messageBus, provider)
	runningServices, setupErr := setupAndStartServices(
		context.Background(),
		cfg,
		agentLoop,
		messageBus,
		"service-setup-test-token",
		listenResult,
		lifecycleCtx,
	)
	if cleanupErr := cleanupFailedGatewayStartup(
		runningServices,
		agentLoop,
		provider,
		messageBus,
		true,
	); cleanupErr != nil {
		t.Fatalf("cleanupFailedGatewayStartup() error = %v", cleanupErr)
	}
	return setupErr
}
