//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	eventgithubpoll "github.com/sipeed/picoclaw/pkg/eventing/githubpoll"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/netbind"
)

func TestSetupAndStartServicesInitializesEventMCPBehindStartupBarrier(t *testing.T) {
	mcpServer := gatewayStartupMCPTestServer(t, []string{
		eventgithubpoll.ListNotificationsTool,
		eventgithubpoll.PullRequestReadTool,
	})
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"github": {
			Enabled:           true,
			Format:            config.EventWebhookFormatGitHub,
			PollNotifications: true,
		},
	}
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			eventgithubpoll.DefaultMCPServer: {
				Enabled: true,
				Type:    "http",
				URL:     mcpServer.URL,
			},
		},
	}
	cfg.Tools.Cron.Enabled = false
	cfg.Heartbeat.Enabled = false

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg.Gateway.Port = port
	listenResult := netbind.OpenResult{
		Listeners: []net.Listener{listener},
		BindHosts: []string{"127.0.0.1"},
		Port:      strconv.Itoa(port),
		ProbeHost: "127.0.0.1",
	}

	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "not used"},
		agent.WithRuntimeStartupBarrier(),
	)
	var runningServices *services
	releaseStartup := func() {}
	t.Cleanup(func() {
		releaseStartup()
		if runningServices != nil {
			if cleanupErr := stopAndCleanupServices(
				runningServices,
				5*time.Second,
				false,
			); cleanupErr != nil {
				t.Errorf("stopAndCleanupServices() error = %v", cleanupErr)
			}
		}
		loop.ReleaseRuntimeStartupBarrier()
		loop.Stop()
		messageBus.Close()
		loop.Close()
		_ = listener.Close()
	})

	startupCtx, acquiredRelease, err := loop.AcquireRuntimeStartupUse(
		context.Background(),
		cfg,
	)
	if err != nil {
		t.Fatalf("AcquireRuntimeStartupUse() error = %v", err)
	}
	releaseStartup = acquiredRelease

	type setupResult struct {
		services *services
		err      error
	}
	done := make(chan setupResult, 1)
	go func() {
		running, setupErr := setupAndStartServices(
			startupCtx,
			cfg,
			loop,
			messageBus,
			"startup-test-token",
			listenResult,
		)
		done <- setupResult{services: running, err: setupErr}
	}()

	timedOut := false
	select {
	case result := <-done:
		runningServices = result.services
		err = result.err
	case <-time.After(5 * time.Second):
		timedOut = true
		releaseStartup()
		loop.ReleaseRuntimeStartupBarrier()
		select {
		case result := <-done:
			runningServices = result.services
			err = result.err
		case <-time.After(5 * time.Second):
			t.Fatal("setup remained blocked after releasing the startup barrier")
		}
	}
	releaseStartup()

	if timedOut {
		t.Fatal("service setup blocked on its own startup runtime barrier")
	}
	if err != nil {
		t.Fatalf("setupAndStartServices() error = %v", err)
	}
	if runningServices == nil || runningServices.EventAutomation == nil {
		t.Fatalf("setup services = %#v, want event automation runtime", runningServices)
	}
	for _, toolName := range []string{
		eventgithubpoll.ListNotificationsTool,
		eventgithubpoll.PullRequestReadTool,
	} {
		canonicalName := picomcp.CanonicalToolName(
			eventgithubpoll.DefaultMCPServer,
			toolName,
		)
		if !loop.GetRegistry().GetDefaultAgent().Tools.HasRegistered(canonicalName) {
			t.Fatalf("startup MCP tool %q was not registered", canonicalName)
		}
	}
}

func gatewayStartupMCPTestServer(
	t *testing.T,
	toolNames []string,
) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "gateway-startup-test",
		Version: "1.0.0",
	}, nil)
	for _, toolName := range toolNames {
		sdkmcp.AddTool(
			server,
			&sdkmcp.Tool{Name: toolName},
			func(
				context.Context,
				*sdkmcp.CallToolRequest,
				map[string]any,
			) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{
						&sdkmcp.TextContent{Text: `{"notifications":[]}`},
					},
				}, nil, nil
			},
		)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		nil,
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}
