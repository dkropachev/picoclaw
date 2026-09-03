package gateway

import (
	"context"
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
