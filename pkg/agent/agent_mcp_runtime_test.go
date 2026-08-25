package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
)

func TestMCPRuntimeConcurrentDoInitializesOnce(t *testing.T) {
	const workers = 64

	var runtime mcpRuntime
	var calls atomic.Int32
	start := make(chan struct{})
	initialized := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, workers)

	for range workers {
		go func() {
			<-start
			results <- runtime.do(func() {
				calls.Add(1)
				close(initialized)
				<-release
			})
		}()
	}

	close(start)
	select {
	case <-initialized:
	case <-time.After(time.Second):
		t.Fatal("MCP initializer did not start")
	}
	close(release)
	for range workers {
		if err := <-results; err != nil {
			t.Fatalf("mcpRuntime.do() error = %v, want nil", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("MCP initializer calls = %d, want 1", got)
	}
}

func TestMCPRuntimeResetWaitsForBlockedInitialization(t *testing.T) {
	var runtime mcpRuntime
	manager := mcp.NewManager()
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close MCP manager: %v", err)
		}
	})

	initialized := make(chan struct{})
	release := make(chan struct{})
	initDone := make(chan error, 1)
	go func() {
		initDone <- runtime.do(func() {
			runtime.setManager(manager)
			close(initialized)
			<-release
		})
	}()
	<-initialized

	resetDone := make(chan *mcp.Manager, 1)
	go func() {
		resetDone <- runtime.reset()
	}()
	select {
	case <-resetDone:
		t.Fatal("reset returned while MCP initialization was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("mcpRuntime.do() error = %v, want nil", err)
	}
	if got := <-resetDone; got != manager {
		t.Fatalf("reset manager = %p, want %p", got, manager)
	}
	if runtime.hasManager() {
		t.Fatal("reset retained initialized MCP manager")
	}

	reruns := 0
	if err := runtime.do(func() { reruns++ }); err != nil {
		t.Fatalf("mcpRuntime.do() after reset error = %v", err)
	}
	if reruns != 1 {
		t.Fatalf("initializer reruns after reset = %d, want 1", reruns)
	}
}

func TestMCPRuntimeTakeManagerWaitsForBlockedInitialization(t *testing.T) {
	var runtime mcpRuntime
	manager := mcp.NewManager()
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close MCP manager: %v", err)
		}
	})

	initialized := make(chan struct{})
	release := make(chan struct{})
	initDone := make(chan error, 1)
	go func() {
		initDone <- runtime.do(func() {
			runtime.setManager(manager)
			close(initialized)
			<-release
		})
	}()
	<-initialized

	takeDone := make(chan *mcp.Manager, 1)
	go func() {
		takeDone <- runtime.takeManager()
	}()
	select {
	case <-takeDone:
		t.Fatal("takeManager returned while MCP initialization was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("mcpRuntime.do() error = %v, want nil", err)
	}
	if got := <-takeDone; got != manager {
		t.Fatalf("takeManager manager = %p, want %p", got, manager)
	}
}

func TestMCPRuntimeCachesErrorUntilReset(t *testing.T) {
	var runtime mcpRuntime
	wantErr := errors.New("MCP initialization failed")
	calls := 0

	if err := runtime.do(func() {
		calls++
		runtime.setInitErr(wantErr)
	}); !errors.Is(err, wantErr) {
		t.Fatalf("first mcpRuntime.do() error = %v, want %v", err, wantErr)
	}
	if err := runtime.do(func() { calls++ }); !errors.Is(err, wantErr) {
		t.Fatalf("cached mcpRuntime.do() error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("initializer calls before reset = %d, want 1", calls)
	}
	if manager := runtime.reset(); manager != nil {
		t.Fatalf("reset manager = %p, want nil", manager)
	}
	if err := runtime.do(func() { calls++ }); err != nil {
		t.Fatalf("mcpRuntime.do() after reset error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("initializer calls after reset = %d, want 2", calls)
	}
}

func TestEnsureMCPInitializedForGenerationDoesNotMixLoopState(t *testing.T) {
	serverA := workflowDependencyMCPTestServer(t, []string{"tool_a"})
	serverB := workflowDependencyMCPTestServer(t, []string{"tool_b"})
	cfgA, registryA := mcpTestGeneration(t, "server_a", serverA.URL)
	cfgB, registryB := mcpTestGeneration(t, "server_b", serverB.URL)

	loop := &AgentLoop{cfg: cfgB, registry: registryB}
	defer loop.Close()
	defer registryA.Close()

	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(),
		cfgA,
		registryA,
	); err != nil {
		t.Fatalf("ensureMCPInitializedForGeneration() error = %v", err)
	}

	wantName := mcp.CanonicalToolName("server_a", "tool_a")
	if !registryA.GetDefaultAgent().Tools.HasRegistered(wantName) {
		t.Fatalf("captured generation A tool %q was not registered", wantName)
	}
	for _, name := range []string{
		mcp.CanonicalToolName("server_a", "tool_a"),
		mcp.CanonicalToolName("server_b", "tool_b"),
	} {
		if registryB.GetDefaultAgent().Tools.HasRegistered(name) {
			t.Fatalf("MCP initializer leaked tool %q into mutable loop registry B", name)
		}
	}
}

func TestEnsureMCPInitializedHoldsGenerationAcrossReload(t *testing.T) {
	serverA, listStarted, releaseList := blockingMCPListTestServer(
		t,
		"tool_a",
	)
	serverB := workflowDependencyMCPTestServer(t, []string{"tool_b"})
	cfgA := mcpTestConfig(t, "server_a", serverA.URL)
	cfgB := mcpTestConfig(t, "server_b", serverB.URL)
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfgA, messageBus, &mockProvider{})
	defer messageBus.Close()
	defer loop.Close()

	registryA := loop.GetRegistry()
	agentA := registryA.GetDefaultAgent()
	ensureDone := make(chan error, 1)
	go func() {
		ensureDone <- loop.ensureMCPInitialized(context.Background())
	}()
	select {
	case <-listStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("generation A MCP tools/list did not start")
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loop.ReloadProviderAndConfig(
			context.Background(),
			&mockProvider{},
			cfgB,
		)
	}()
	waitForMCPReloadPause(t, loop)
	select {
	case err := <-reloadDone:
		t.Fatalf("reload crossed blocked generation A MCP initialization: %v", err)
	default:
	}

	releaseList()
	select {
	case err := <-ensureDone:
		if err != nil {
			t.Fatalf("generation A ensureMCPInitialized() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generation A MCP initialization did not finish")
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reload did not finish after generation A MCP initialization drained")
	}

	registryB := loop.GetRegistry()
	if loop.GetConfig() != cfgB || registryB == registryA {
		t.Fatal("reload did not publish the exact generation B config/registry pair")
	}
	nameA := mcp.CanonicalToolName("server_a", "tool_a")
	nameB := mcp.CanonicalToolName("server_b", "tool_b")
	if agentA.Tools.Count() != 0 {
		t.Fatal("reload did not retire generation A tool registry")
	}
	agentB := registryB.GetDefaultAgent()
	if agentB.Tools.HasRegistered(nameA) {
		t.Fatalf("generation A MCP tool %q leaked into generation B", nameA)
	}
	if !agentB.Tools.HasRegistered(nameB) {
		t.Fatalf("generation B MCP tool %q was not initialized after reset", nameB)
	}
	if loop.mcp.getManager() == nil {
		t.Fatal("generation B MCP manager was not retained")
	}
}

func TestEnsureMCPInitializedReusesOwnedRuntimeLease(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"owned_lease"})
	cfg := mcpTestConfig(t, "owned", server.URL)
	loop := &AgentLoop{cfg: cfg, registry: NewAgentRegistry(cfg, nil)}
	defer loop.Close()

	leaseCtx, releaseRuntime, err := loop.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	defer releaseRuntime()

	// Model the reload boundary that is closed to new admissions. The existing
	// lease must still reach MCP initialization without attempting admission a
	// second time.
	setMCPTestRuntimePause(loop, true)
	done := make(chan error, 1)
	go func() {
		done <- loop.ensureMCPInitialized(leaseCtx)
	}()

	select {
	case err := <-done:
		setMCPTestRuntimePause(loop, false)
		if err != nil {
			t.Fatalf("ensureMCPInitialized() error = %v", err)
		}
	case <-time.After(time.Second):
		setMCPTestRuntimePause(loop, false)
		<-done
		t.Fatal("ensureMCPInitialized deadlocked while context owned the runtime lease")
	}

	loop.runtimeGateMu.Lock()
	active := loop.runtimeGateActive
	loop.runtimeGateMu.Unlock()
	if active != 1 {
		t.Fatalf("active runtime leases = %d, want the original lease only", active)
	}
}

func TestEnsureMCPInitializedStaticNoOpBypassesStoppedAdmission(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.Config, *AgentRegistry)
	}{
		{
			name: "disabled",
			configure: func(cfg *config.Config, _ *AgentRegistry) {
				cfg.Tools.MCP = config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: false}}
			},
		},
		{
			name: "no servers",
			configure: func(cfg *config.Config, _ *AgentRegistry) {
				cfg.Tools.MCP = config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: true}}
			},
		},
		{
			name: "filtered servers",
			configure: func(cfg *config.Config, registry *AgentRegistry) {
				cfg.Tools.MCP = mcpTestConfig(t, "blocked", "http://127.0.0.1:1").Tools.MCP
				registry.GetDefaultAgent().MCPServerAllowlist = map[string]struct{}{}
			},
		},
		{
			name: "all servers disabled",
			configure: func(cfg *config.Config, _ *AgentRegistry) {
				cfg.Tools.MCP = mcpTestConfig(t, "disabled", "http://127.0.0.1:1").Tools.MCP
				server := cfg.Tools.MCP.Servers["disabled"]
				server.Enabled = false
				cfg.Tools.MCP.Servers["disabled"] = server
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			registry := NewAgentRegistry(cfg, nil)
			test.configure(cfg, registry)
			loop := &AgentLoop{cfg: cfg, registry: registry}
			defer loop.Close()
			loop.runtimeGateMu.Lock()
			loop.runtimeGatePaused = true
			loop.runtimeGatePauses = 1
			loop.runtimeGateStopped = true
			loop.runtimeGateMu.Unlock()

			beforeTools := registry.GetDefaultAgent().Tools.Count()
			if err := loop.ensureMCPInitialized(context.Background()); err != nil {
				t.Fatalf("static no-op initialization error = %v", err)
			}
			loop.runtimeGateMu.Lock()
			active := loop.runtimeGateActive
			loop.runtimeGateMu.Unlock()
			if active != 0 || loop.mcp.hasManager() ||
				registry.GetDefaultAgent().Tools.Count() != beforeTools {
				t.Fatalf(
					"static no-op mutated runtime: active=%d manager=%t tools=%d/%d",
					active,
					loop.mcp.hasManager(),
					registry.GetDefaultAgent().Tools.Count(),
					beforeTools,
				)
			}
			initializerCalls := 0
			if err := loop.mcp.do(func() { initializerCalls++ }); err != nil || initializerCalls != 1 {
				t.Fatalf("static no-op consumed initOnce: calls=%d error=%v", initializerCalls, err)
			}
		})
	}
}

func TestMCPGenerationLifecycleValidationEdges(t *testing.T) {
	var nilLoop *AgentLoop
	if err := nilLoop.EnsureMCPInitialized(context.Background()); err == nil {
		t.Fatal("nil public AgentLoop initialized MCP")
	}
	if err := nilLoop.ensureMCPInitialized(context.Background()); err == nil {
		t.Fatal("nil internal AgentLoop initialized MCP")
	}
	loop := &AgentLoop{}
	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(), nil, nil,
	); err == nil {
		t.Fatal("nil config generation initialized MCP")
	}
	cfg := mcpTestConfig(t, "configured", "http://127.0.0.1:1")
	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(), cfg, nil,
	); err == nil {
		t.Fatal("nil registry generation initialized MCP")
	}
	if mcpGenerationNeedsRuntimeLease(nil, nil) ||
		mcpGenerationNeedsRuntimeLease(cfg, nil) {
		t.Fatal("incomplete MCP generation requested a runtime lease")
	}
	noOpCfg := config.DefaultConfig()
	noOpCfg.Agents.Defaults.Workspace = t.TempDir()
	noOpCfg.Tools.MCP = config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: false}}
	loop.cfg = noOpCfg
	loop.registry = NewAgentRegistry(noOpCfg, nil)
	defer loop.registry.Close()
	if err := loop.EnsureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("public no-op MCP initialization error = %v", err)
	}
}

func mcpTestGeneration(
	t *testing.T,
	serverName string,
	serverURL string,
) (*config.Config, *AgentRegistry) {
	t.Helper()
	cfg := mcpTestConfig(t, serverName, serverURL)
	return cfg, NewAgentRegistry(cfg, nil)
}

func mcpTestConfig(
	t *testing.T,
	serverName string,
	serverURL string,
) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			serverName: {
				Enabled: true,
				Type:    "http",
				URL:     serverURL,
			},
		},
	}
	return cfg
}

func setMCPTestRuntimePause(loop *AgentLoop, paused bool) {
	loop.runtimeGateMu.Lock()
	if paused {
		loop.runtimeGatePauses = 1
	} else {
		loop.runtimeGatePauses = 0
	}
	loop.runtimeGatePaused = paused
	loop.signalRuntimeGateChangedLocked()
	loop.runtimeGateMu.Unlock()
}

func waitForMCPReloadPause(t *testing.T, loop *AgentLoop) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		loop.runtimeGateMu.Lock()
		paused := loop.runtimeGatePaused
		active := loop.runtimeGateActive
		loop.runtimeGateMu.Unlock()
		if paused && active == 1 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf(
				"reload pause state = paused %v, active %d; want blocked on one MCP lease",
				paused,
				active,
			)
		case <-ticker.C:
		}
	}
}

func blockingMCPListTestServer(
	t *testing.T,
	toolName string,
) (*httptest.Server, <-chan struct{}, func()) {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "generation-blocking-test",
		Version: "1.0.0",
	}, nil)
	sdkmcp.AddTool(
		server,
		&sdkmcp.Tool{Name: toolName},
		func(
			context.Context,
			*sdkmcp.CallToolRequest,
			map[string]any,
		) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
			}, nil, nil
		},
	)
	baseHandler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		nil,
	)
	listStarted := make(chan struct{})
	listRelease := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "read MCP request", http.StatusBadRequest)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var call struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &call) == nil && call.Method == "tools/list" {
			startOnce.Do(func() { close(listStarted) })
			select {
			case <-listRelease:
			case <-request.Context().Done():
				return
			}
		}
		baseHandler.ServeHTTP(writer, request)
	})
	httpServer := httptest.NewServer(handler)
	release := func() { releaseOnce.Do(func() { close(listRelease) }) }
	t.Cleanup(httpServer.Close)
	t.Cleanup(release)
	return httpServer, listStarted, release
}
