package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunServesHealthAndStopsWithContext(t *testing.T) {
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("STREAMABLE_LISTEN_ADDRESS", "127.0.0.1:0")
	t.Setenv("STREAMABLE_READY_FILE", readyFile)
	t.Setenv("STREAMABLE_JSON_RESPONSE", "true")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runResult := make(chan error, 1)
	go func() {
		runResult <- run(ctx)
	}()

	var response *http.Response
	var serverAddress string
	healthClient := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(readyFile)
		if err == nil {
			serverAddress = strings.TrimSpace(string(data))
			response, err = healthClient.Get("http://" + serverAddress + "/healthz")
			if err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response == nil {
		t.Fatal("server did not publish a reachable health endpoint")
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("health response = %d %q", response.StatusCode, body)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "fixture-test", Version: "1.0.0"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             "http://" + serverAddress + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect to MCP fixture: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "covered echo"},
	})
	if err != nil {
		t.Fatalf("call echo tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("echo content count = %d", len(result.Content))
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok || content.Text != "covered echo" {
		t.Fatalf("echo content = %#v", result.Content[0])
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close MCP session: %v", err)
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not stop after cancellation")
	}
}

func TestRunRejectsInvalidOrUnavailableAddress(t *testing.T) {
	t.Run("non-loopback", func(t *testing.T) {
		t.Setenv("STREAMABLE_LISTEN_ADDRESS", "0.0.0.0:0")
		if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "IPv4 loopback") {
			t.Fatalf("run() error = %v", err)
		}
	})

	t.Run("address in use", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		t.Setenv("STREAMABLE_LISTEN_ADDRESS", listener.Addr().String())
		if err = run(context.Background()); err == nil || !strings.Contains(err.Error(), "listen on") {
			t.Fatalf("run() error = %v", err)
		}
	})
}

func TestRunRejectsUnwritableReadyFile(t *testing.T) {
	t.Setenv("STREAMABLE_LISTEN_ADDRESS", "127.0.0.1:0")
	t.Setenv("STREAMABLE_READY_FILE", filepath.Join(t.TempDir(), "missing", "ready"))
	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "publish ready address") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestEnvBool(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    string
		fallback bool
		want     bool
	}{
		{name: "empty true fallback", fallback: true, want: true},
		{name: "empty false fallback", fallback: false, want: false},
		{name: "true", value: " YES ", want: true},
		{name: "false", value: "off", fallback: true, want: false},
		{name: "invalid true fallback", value: "sometimes", fallback: true, want: true},
		{name: "invalid false fallback", value: "sometimes", fallback: false, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("STREAMABLE_TEST_BOOL", test.value)
			if got := envBool("STREAMABLE_TEST_BOOL", test.fallback); got != test.want {
				t.Fatalf("envBool() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEnvString(t *testing.T) {
	t.Setenv("STREAMABLE_TEST_VALUE", "")
	if got := envString("STREAMABLE_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("empty value = %q, want fallback", got)
	}
	t.Setenv("STREAMABLE_TEST_VALUE", " 127.0.0.1:0 \n")
	if got := envString("STREAMABLE_TEST_VALUE", "fallback"); got != "127.0.0.1:0" {
		t.Fatalf("trimmed value = %q", got)
	}
}

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.0.0.1:8080"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"", ":0", "0.0.0.0:0", "localhost:0", "[::1]:0"} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) succeeded", address)
		}
	}
}

func TestPublishReadyAddress(t *testing.T) {
	t.Setenv("STREAMABLE_READY_FILE", "")
	if err := publishReadyAddress("127.0.0.1:1234"); err != nil {
		t.Fatalf("disabled readiness error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "ready")
	t.Setenv("STREAMABLE_READY_FILE", path)
	if err := publishReadyAddress("127.0.0.1:1234"); err != nil {
		t.Fatalf("publishReadyAddress() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "127.0.0.1:1234\n" {
		t.Fatalf("ready address = %q", data)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
		var mode any
		if info != nil {
			mode = info.Mode().Perm()
		}
		t.Fatalf("ready file mode = %v, error = %v", mode, statErr)
	}

	directoryPath := filepath.Join(t.TempDir(), "ready-directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STREAMABLE_READY_FILE", directoryPath)
	if err := publishReadyAddress("127.0.0.1:1234"); err == nil || !strings.Contains(err.Error(), "publish readiness") {
		t.Fatalf("directory target error = %v", err)
	}
	if _, err := os.Stat(directoryPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary ready file remains after rename failure: %v", err)
	}
}
