package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowAuthoringTestTool struct {
	name              string
	parameters        map[string]any
	panicName         bool
	panicParameters   bool
	panicDescription  bool
	parametersStarted chan struct{}
	parametersRelease chan struct{}
}

func (tool *workflowAuthoringTestTool) Name() string {
	if tool.panicName {
		panic("private name panic")
	}
	return tool.name
}

func (tool *workflowAuthoringTestTool) Description() string {
	if tool.panicDescription {
		panic("private description panic")
	}
	return "private description"
}

func (tool *workflowAuthoringTestTool) Parameters() map[string]any {
	if tool.parametersStarted != nil {
		select {
		case <-tool.parametersStarted:
		default:
			close(tool.parametersStarted)
		}
	}
	if tool.parametersRelease != nil {
		<-tool.parametersRelease
	}
	if tool.panicParameters {
		panic("private parameter panic")
	}
	return tool.parameters
}

func (*workflowAuthoringTestTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("")
}

func TestWorkflowAuthoringCapabilitiesUsesRegistryKeysAndSanitizesShapes(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	loop := workflowDependencyTestLoop(cfg)
	defaultAgent := loop.registry.GetDefaultAgent()

	safe := &workflowAuthoringTestTool{
		name: "zeta",
		parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "private schema description",
				},
			},
			"required": []string{"message"},
		},
		panicDescription: true,
	}
	defaultAgent.Tools.Register(safe)
	// Name drift and even a later panic cannot alter the exact executable
	// registry key visited by capability projection.
	safe.name = "unsafe\u202Edrift"
	safe.panicName = true

	badShape := &workflowAuthoringTestTool{
		name: "alpha",
		parameters: map[string]any{
			"$ref": "file:///private/schema.json",
		},
	}
	defaultAgent.Tools.Register(badShape)
	panicShape := &workflowAuthoringTestTool{
		name:            "beta",
		panicParameters: true,
	}
	defaultAgent.Tools.Register(panicShape)
	defaultAgent.Tools.Register(&workflowAuthoringTestTool{
		name:       "workflow",
		parameters: map[string]any{},
	})
	hidden := &workflowAuthoringTestTool{
		name:       "hidden",
		parameters: map[string]any{},
	}
	defaultAgent.Tools.RegisterHidden(hidden)
	defaultAgent.Tools.PromoteTools([]string{"hidden"}, 3)

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPDisabled {
		t.Fatalf("mcp_status = %q, want disabled", catalog.MCPStatus)
	}
	names := make([]string, 0, len(catalog.Tools))
	for _, capability := range catalog.Tools {
		names = append(names, capability.Name)
		if capability.Readiness != workflows.WorkflowDependencyReadinessReady {
			t.Fatalf("%s readiness = %q", capability.Name, capability.Readiness)
		}
	}
	if !sort.StringsAreSorted(names) ||
		!containsWorkflowAuthoringTestString(names, "alpha") ||
		!containsWorkflowAuthoringTestString(names, "beta") ||
		!containsWorkflowAuthoringTestString(names, "zeta") ||
		containsWorkflowAuthoringTestString(names, "workflow") ||
		containsWorkflowAuthoringTestString(names, "hidden") {
		t.Fatalf("tool names = %#v, want sorted core keys without workflow/hidden", names)
	}
	byName := make(map[string]workflows.WorkflowAuthoringToolCapability, len(catalog.Tools))
	for _, capability := range catalog.Tools {
		byName[capability.Name] = capability
	}
	if byName["alpha"].ParameterShapeProjected ||
		byName["alpha"].ParameterShape != nil ||
		byName["beta"].ParameterShapeProjected ||
		byName["beta"].ParameterShape != nil {
		t.Fatalf("unsafe parameter shapes were projected: %#v", catalog.Tools)
	}
	zeta := byName["zeta"]
	if !zeta.ParameterShapeProjected ||
		zeta.ParameterShape == nil ||
		len(zeta.ParameterShape.Properties) != 1 ||
		!zeta.ParameterShape.Properties[0].Required {
		t.Fatalf("zeta parameter shape = %#v", zeta.ParameterShape)
	}
	if !reflect.DeepEqual(catalog.Limits, []workflows.WorkflowAuthoringLimitCode{
		workflows.WorkflowAuthoringParameterShapesOmitted,
	}) {
		t.Fatalf("limits = %#v", catalog.Limits)
	}
	if catalog.Complete {
		t.Fatal("catalog with omitted parameter shapes reported complete")
	}
	if len(catalog.Agents) != 1 ||
		catalog.Agents[0].Target != "agent/main" ||
		!catalog.Agents[0].IsDefault {
		t.Fatalf("agents = %#v", catalog.Agents)
	}
	if got := len(catalog.Functions); got != workflows.MaxWorkflowAuthoringFunctions {
		t.Fatalf("functions = %d, want %d", got, workflows.MaxWorkflowAuthoringFunctions)
	}
	if _, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog); !ok {
		t.Fatal("constructed catalog did not pass shared validation")
	}
}

func containsWorkflowAuthoringTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestWorkflowAuthoringCapabilitiesEagerAndDeferredMCP(t *testing.T) {
	t.Run("eager exact identity", func(t *testing.T) {
		loop := mcpRegistryTestLoop(t, false, []string{"issues.list"})
		defer loop.Close()
		if err := loop.ensureMCPInitialized(context.Background()); err != nil {
			t.Fatalf("ensureMCPInitialized() error = %v", err)
		}

		catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
		if err != nil {
			t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
		}
		if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
			len(catalog.MCPTools) != 1 {
			t.Fatalf("MCP catalog = status %q, tools %#v", catalog.MCPStatus, catalog.MCPTools)
		}
		capability := catalog.MCPTools[0]
		if capability.Server != "github" ||
			capability.Tool != "issues.list" ||
			capability.Target != "mcp/github/issues.list" ||
			capability.Readiness != workflows.WorkflowDependencyReadinessReady {
			t.Fatalf("MCP capability = %#v", capability)
		}
	})

	t.Run("deferred excluded", func(t *testing.T) {
		loop := mcpRegistryTestLoop(t, true, []string{"issues.list"})
		defer loop.Close()
		if err := loop.ensureMCPInitialized(context.Background()); err != nil {
			t.Fatalf("ensureMCPInitialized() error = %v", err)
		}

		catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
		if err != nil {
			t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
		}
		if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
			len(catalog.MCPTools) != 0 {
			t.Fatalf("deferred MCP catalog = status %q, tools %#v", catalog.MCPStatus, catalog.MCPTools)
		}
	})

	t.Run("slash tool identity excluded", func(t *testing.T) {
		loop := mcpRegistryTestLoop(t, false, []string{"folder/read"})
		defer loop.Close()
		if err := loop.ensureMCPInitialized(context.Background()); err != nil {
			t.Fatalf("ensureMCPInitialized() error = %v", err)
		}

		catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
		if err != nil {
			t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
		}
		if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
			len(catalog.MCPTools) != 0 ||
			!reflect.DeepEqual(catalog.Limits, []workflows.WorkflowAuthoringLimitCode{
				workflows.WorkflowAuthoringUnsafeFieldsOmitted,
			}) {
			t.Fatalf("ambiguous MCP catalog = %#v", catalog)
		}
	})

	t.Run("ready tools use deterministic bounded selection", func(t *testing.T) {
		names := make([]string, 0, workflows.MaxWorkflowAuthoringMCPTools+1)
		for index := 0; index <= workflows.MaxWorkflowAuthoringMCPTools; index++ {
			names = append(names, fmt.Sprintf("tool-%03d", index))
		}
		loop := mcpRegistryTestLoop(t, false, names)
		defer loop.Close()
		if err := loop.ensureMCPInitialized(context.Background()); err != nil {
			t.Fatalf("ensureMCPInitialized() error = %v", err)
		}

		catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
		if err != nil {
			t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
		}
		if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
			len(catalog.MCPTools) != workflows.MaxWorkflowAuthoringMCPTools ||
			!reflect.DeepEqual(catalog.Limits, []workflows.WorkflowAuthoringLimitCode{
				workflows.WorkflowAuthoringMCPToolsTruncated,
			}) {
			t.Fatalf("bounded MCP catalog = %#v", catalog)
		}
		for index, capability := range catalog.MCPTools {
			want := fmt.Sprintf("tool-%03d", index)
			if capability.Tool != want {
				t.Fatalf("MCP tool %d = %q, want %q", index, capability.Tool, want)
			}
		}
	})
}

func TestWorkflowAuthoringCapabilitiesRejectsCollisionBeforeBoundedSelection(t *testing.T) {
	names := make([]string, 0, workflows.MaxWorkflowAuthoringMCPTools+3)
	for index := 0; index <= workflows.MaxWorkflowAuthoringMCPTools; index++ {
		names = append(names, fmt.Sprintf("tool-%03d", index))
	}
	names = append(names, "Search", "search")
	loop := workflowAuthoringRawMCPTestLoop(t, names)

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
		len(catalog.MCPTools) != 0 ||
		!containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringUnsafeFieldsOmitted,
		) ||
		containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringMCPToolsTruncated,
		) {
		t.Fatalf("colliding MCP catalog = %#v", catalog)
	}
	if _, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog); !ok {
		t.Fatal("colliding partial catalog did not pass shared validation")
	}
	if readiness := loop.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: "github/Search",
		},
	); readiness != workflows.WorkflowDependencyReadinessNameCollision {
		t.Fatalf("collision readiness = %q, want name_collision", readiness)
	}
}

func TestWorkflowAuthoringCapabilitiesDeduplicatesExactMCPIdentity(t *testing.T) {
	names := make([]string, workflows.MaxWorkflowAuthoringMCPTools+1)
	for index := range names {
		names[index] = "echo"
	}
	loop := workflowAuthoringRawMCPTestLoop(t, names)

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
		len(catalog.MCPTools) != 1 ||
		catalog.MCPTools[0].Server != "github" ||
		catalog.MCPTools[0].Tool != "echo" ||
		containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringMCPToolsTruncated,
		) ||
		containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringUnsafeFieldsOmitted,
		) {
		t.Fatalf("exact-duplicate MCP catalog = %#v", catalog)
	}
	if _, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog); !ok {
		t.Fatal("exact-duplicate catalog did not pass shared validation")
	}
	if readiness := loop.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: "github/echo",
		},
	); readiness != workflows.WorkflowDependencyReadinessReady {
		t.Fatalf("exact-duplicate readiness = %q, want ready", readiness)
	}
}

func TestWorkflowAuthoringCapabilitiesRanksOnlyReadyMCPTools(t *testing.T) {
	names := make(
		[]string,
		0,
		workflows.MaxWorkflowAuthoringMCPTools*2+1,
	)
	for index := 0; index <= workflows.MaxWorkflowAuthoringMCPTools; index++ {
		names = append(names, fmt.Sprintf("a-unregistered-%03d", index))
	}
	for index := 0; index < workflows.MaxWorkflowAuthoringMCPTools; index++ {
		names = append(names, fmt.Sprintf("z-ready-%03d", index))
	}
	loop := workflowAuthoringRawMCPTestLoop(t, names)
	defaultAgent := loop.registry.GetDefaultAgent()
	for index := 0; index <= workflows.MaxWorkflowAuthoringMCPTools; index++ {
		defaultAgent.Tools.Unregister(picomcp.CanonicalToolName(
			"github",
			fmt.Sprintf("a-unregistered-%03d", index),
		))
	}

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
		len(catalog.MCPTools) != workflows.MaxWorkflowAuthoringMCPTools ||
		containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringMCPToolsTruncated,
		) {
		t.Fatalf("ready-before-rank MCP catalog = %#v", catalog)
	}
	for index, capability := range catalog.MCPTools {
		want := fmt.Sprintf("z-ready-%03d", index)
		if capability.Tool != want {
			t.Fatalf("MCP tool %d = %q, want %q", index, capability.Tool, want)
		}
	}
	if _, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog); !ok {
		t.Fatal("ready-before-rank catalog did not pass shared validation")
	}
}

func TestWorkflowAuthoringCapabilitiesMarksRegistryMCPIdentityCollisionPartial(
	t *testing.T,
) {
	loop := workflowDependencyConnectedMCPTestLoop(t, map[string][]string{
		"github": {"search"},
	})
	defaultAgent := loop.registry.GetDefaultAgent()
	defaultAgent.Tools.Register(&workflowAuthoringTestTool{
		name:       picomcp.CanonicalToolName("github", "search"),
		parameters: map[string]any{},
	})

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
		len(catalog.MCPTools) != 0 ||
		catalog.Complete ||
		!containsWorkflowAuthoringTestLimit(
			catalog.Limits,
			workflows.WorkflowAuthoringUnsafeFieldsOmitted,
		) {
		t.Fatalf("registry-colliding MCP catalog = %#v", catalog)
	}
	if _, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog); !ok {
		t.Fatal("registry-colliding partial catalog did not pass shared validation")
	}
	if readiness := loop.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: "github/search",
		},
	); readiness != workflows.WorkflowDependencyReadinessNameCollision {
		t.Fatalf("registry collision readiness = %q, want name_collision", readiness)
	}
}

func TestSelectReadyWorkflowAuthoringMCPToolsPropagatesMidScanCancellation(
	t *testing.T,
) {
	loop := workflowDependencyConnectedMCPTestLoop(t, map[string][]string{
		"github": {"first", "second", "third"},
	})
	base, cancel := context.WithCancel(context.Background())
	ctx := &workflowAuthoringCancelAfterChecksContext{
		Context:   base,
		remaining: 3,
		cancel:    cancel,
	}

	candidates, eligible, unsafe, err := loop.selectReadyWorkflowAuthoringMCPTools(
		ctx,
		loop.registry.GetDefaultAgent(),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("selectReadyWorkflowAuthoringMCPTools() error = %v, want canceled", err)
	}
	if candidates != nil || eligible != 0 || unsafe {
		t.Fatalf(
			"canceled MCP selection = %#v, %d, %t; want empty",
			candidates,
			eligible,
			unsafe,
		)
	}
}

type workflowAuthoringCancelAfterChecksContext struct {
	context.Context
	remaining int
	cancel    context.CancelFunc
}

func (ctx *workflowAuthoringCancelAfterChecksContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func containsWorkflowAuthoringTestLimit(
	values []workflows.WorkflowAuthoringLimitCode,
	target workflows.WorkflowAuthoringLimitCode,
) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func workflowAuthoringRawMCPTestLoop(
	t *testing.T,
	toolNames []string,
) *AgentLoop {
	t.Helper()
	httpServer := workflowAuthoringRawMCPTestServer(t, toolNames)
	serverConfig := config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     httpServer.URL,
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"github": serverConfig,
		},
	}

	manager := picomcp.NewManager()
	if err := manager.ConnectServer(context.Background(), "github", serverConfig); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close MCP manager: %v", err)
		}
	})

	loop := workflowDependencyTestLoop(cfg)
	loop.mcp.setManager(manager)
	loop.mcp.initOnce.Do(func() {})
	defaultAgent := loop.registry.GetDefaultAgent()
	connection, connected := manager.GetServer("github")
	if !connected || connection == nil {
		t.Fatal("raw MCP manager did not retain github connection")
	}
	registeredIdentities := make(map[picomcp.ToolIdentity]struct{})
	for _, tool := range connection.Tools {
		identity := picomcp.ToolIdentity{Server: "github", Tool: tool.Name}
		if _, registered := registeredIdentities[identity]; registered {
			continue
		}
		registeredIdentities[identity] = struct{}{}
		defaultAgent.Tools.Register(tools.NewMCPTool(manager, "github", tool))
	}
	return loop
}

func workflowAuthoringRawMCPTestServer(
	t *testing.T,
	toolNames []string,
) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var call struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			http.Error(writer, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		if call.Method == "notifications/initialized" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch call.Method {
		case "initialize":
			result = &sdkmcp.InitializeResult{
				ProtocolVersion: "2025-11-25",
				ServerInfo: &sdkmcp.Implementation{
					Name:    "workflow-authoring-raw-test",
					Version: "1.0.0",
				},
				Capabilities: &sdkmcp.ServerCapabilities{
					Tools: &sdkmcp.ToolCapabilities{},
				},
			}
		case "tools/list":
			listed := make([]*sdkmcp.Tool, 0, len(toolNames))
			for _, name := range toolNames {
				listed = append(listed, &sdkmcp.Tool{
					Name: name,
					InputSchema: map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				})
			}
			result = &sdkmcp.ListToolsResult{Tools: listed}
		default:
			http.Error(writer, "unexpected JSON-RPC method", http.StatusBadRequest)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Mcp-Session-Id", "workflow-authoring-raw-test")
		if err := json.NewEncoder(writer).Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{
			JSONRPC: "2.0",
			ID:      call.ID,
			Result:  result,
		}); err != nil {
			t.Errorf("encode JSON-RPC response: %v", err)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestWorkflowAuthoringCapabilitiesDoesNotInitializeMCP(t *testing.T) {
	marker := t.TempDir() + "/mcp-command-started"
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"private-server": {
				Enabled: true,
				Command: "sh",
				Args: []string{
					"-c",
					`printf started > "$1"`,
					"workflow-authoring-test",
					marker,
				},
			},
		},
	}
	loop := workflowDependencyTestLoop(cfg)
	loop.runtimeEvents = runtimeevents.NewBus()
	loop.ownsRuntimeEvents = true
	defer loop.Close()
	eventStream, closeEvents := subscribeRuntimeEventsForTest(
		t,
		loop,
		8,
		runtimeevents.KindMCPServerConnecting,
		runtimeevents.KindMCPServerConnected,
		runtimeevents.KindMCPServerFailed,
		runtimeevents.KindMCPToolDiscovered,
	)
	defer closeEvents()
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("MCP runtime unexpectedly initialized before catalog request")
	}

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() base error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPUnavailable ||
		catalog.Complete ||
		len(catalog.MCPTools) != 0 {
		t.Fatalf("partial catalog = %#v", catalog)
	}
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("catalog request initialized or mutated MCP runtime state")
	}
	if events := collectRuntimeEventStream(eventStream); len(events) != 0 {
		t.Fatalf("catalog request emitted MCP events: %#v", events)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("catalog request executed MCP command: %v", statErr)
	}
	encoded, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog)
	if !ok {
		t.Fatal("partial catalog failed validation")
	}
	for _, private := range []string{
		"private-server",
		marker,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("partial catalog leaked %q: %s", private, encoded)
		}
	}
}

func TestWorkflowAuthoringCapabilitiesMCPReadinessNeverRunsInitializer(t *testing.T) {
	loop := mcpRegistryTestLoop(t, false, []string{"issues.list"})
	defer loop.Close()
	if err := loop.ensureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("ensureMCPInitialized() error = %v", err)
	}
	liveManager := loop.mcp.reset()
	if liveManager == nil {
		t.Fatal("normal initialization did not install an MCP manager")
	}
	loop.mcp.setManager(liveManager)

	marker := t.TempDir() + "/unexpected-mcp-reinitialization"
	loop.cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {
			Enabled: true,
			Command: "sh",
			Args: []string{
				"-c",
				`printf started > "$1"`,
				"workflow-authoring-test",
				marker,
			},
		},
	}

	catalog, err := loop.WorkflowAuthoringCapabilities(context.Background())
	if err != nil {
		t.Fatalf("WorkflowAuthoringCapabilities() error = %v", err)
	}
	if catalog.MCPStatus != workflows.WorkflowAuthoringMCPReady ||
		len(catalog.MCPTools) != 1 ||
		loop.mcp.getManager() != liveManager ||
		loop.mcp.getInitErr() != nil {
		t.Fatalf("catalog or MCP state = %#v, manager %p", catalog, loop.mcp.getManager())
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("catalog readiness ran MCP initializer: %v", statErr)
	}
}

func TestWorkflowAuthoringCapabilitiesHoldsRuntimeLeaseAcrossProjection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	loop := workflowDependencyTestLoop(cfg)
	defer loop.Close()

	blocking := &workflowAuthoringTestTool{
		name:              "blocking",
		parameters:        map[string]any{},
		parametersStarted: make(chan struct{}),
		parametersRelease: make(chan struct{}),
	}
	loop.registry.GetDefaultAgent().Tools.Register(blocking)

	catalogDone := make(chan error, 1)
	go func() {
		_, err := loop.WorkflowAuthoringCapabilities(context.Background())
		catalogDone <- err
	}()
	select {
	case <-blocking.parametersStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("parameter projection did not start")
	}

	pauseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if resume, err := loop.PauseRuntimeForReload(pauseCtx); err == nil {
		if resume != nil {
			resume()
		}
		t.Fatal("runtime reload pause completed while catalog projection held lease")
	}
	close(blocking.parametersRelease)
	select {
	case err := <-catalogDone:
		if err != nil {
			t.Fatalf("catalog error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("catalog did not release runtime lease")
	}
}

func TestWorkflowAuthoringCapabilitiesHoldsRuntimeLeaseAcrossMarshal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	loop := workflowDependencyTestLoop(cfg)
	defer loop.Close()

	previousMarshal := marshalWorkflowAuthoringCapabilities
	marshalStarted := make(chan struct{})
	marshalRelease := make(chan struct{})
	marshalWorkflowAuthoringCapabilities = func(
		catalog workflows.WorkflowAuthoringCapabilities,
	) ([]byte, bool) {
		close(marshalStarted)
		<-marshalRelease
		return workflows.MarshalWorkflowAuthoringCapabilities(catalog)
	}
	t.Cleanup(func() {
		marshalWorkflowAuthoringCapabilities = previousMarshal
	})

	catalogDone := make(chan error, 1)
	go func() {
		_, err := loop.WorkflowAuthoringCapabilitiesJSON(context.Background())
		catalogDone <- err
	}()
	select {
	case <-marshalStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog marshal did not start")
	}

	pauseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if resume, err := loop.PauseRuntimeForReload(pauseCtx); err == nil {
		if resume != nil {
			resume()
		}
		t.Fatal("runtime reload pause completed while catalog marshal held lease")
	}
	close(marshalRelease)
	select {
	case err := <-catalogDone:
		if err != nil {
			t.Fatalf("catalog error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("catalog did not release runtime lease after marshal")
	}
}

func TestProjectWorkflowAuthoringAgentsTruncationRetainsDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = make(
		[]config.AgentConfig,
		0,
		workflows.MaxWorkflowAuthoringAgents*4+1,
	)
	for index := 0; index < workflows.MaxWorkflowAuthoringAgents*4; index++ {
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: fmt.Sprintf("agent-%03d", index),
		})
	}
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
		ID:      "z-default",
		Default: true,
	})
	registry := NewAgentRegistry(cfg, nil)
	loop := &AgentLoop{cfg: cfg, registry: registry}

	capabilities, limits, err := loop.projectWorkflowAuthoringAgents(
		context.Background(),
		registry,
		nil,
	)
	if err != nil {
		t.Fatalf("projectWorkflowAuthoringAgents() error = %v", err)
	}
	if len(capabilities) != workflows.MaxWorkflowAuthoringAgents ||
		!reflect.DeepEqual(limits, []workflows.WorkflowAuthoringLimitCode{
			workflows.WorkflowAuthoringAgentsTruncated,
		}) {
		t.Fatalf("truncated agents = %d, limits %#v", len(capabilities), limits)
	}
	defaults := 0
	for _, capability := range capabilities {
		if capability.IsDefault {
			defaults++
			if capability.ID != "z-default" {
				t.Fatalf("default capability = %#v", capability)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("default count = %d, want 1", defaults)
	}
	for index := 0; index < workflows.MaxWorkflowAuthoringAgents-1; index++ {
		want := fmt.Sprintf("agent-%03d", index)
		if capabilities[index].ID != want {
			t.Fatalf("capability %d = %q, want %q", index, capabilities[index].ID, want)
		}
	}
	for index := 1; index < len(capabilities); index++ {
		if capabilities[index-1].ID >= capabilities[index].ID {
			t.Fatalf("agents are not sorted at %d: %#v", index, capabilities)
		}
	}
	again, againLimits, err := loop.projectWorkflowAuthoringAgents(
		context.Background(),
		registry,
		nil,
	)
	if err != nil ||
		!reflect.DeepEqual(again, capabilities) ||
		!reflect.DeepEqual(againLimits, limits) {
		t.Fatalf("second bounded projection = %#v, %#v, %v", again, againLimits, err)
	}
}

func TestProjectWorkflowAuthoringAgentsOmitsNoncanonicalRuntimeIDs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	registry := NewAgentRegistry(cfg, nil)
	defaultAgent := registry.GetDefaultAgent()
	registry.mu.Lock()
	for _, id := range []string{
		"Main",
		"foo/bar",
		"foo.bar",
		"á",
		strings.Repeat("a", 65),
	} {
		registry.agents[id] = defaultAgent
	}
	registry.mu.Unlock()
	loop := &AgentLoop{cfg: cfg, registry: registry}

	capabilities, limits, err := loop.projectWorkflowAuthoringAgents(
		context.Background(),
		registry,
		nil,
	)
	if err != nil {
		t.Fatalf("projectWorkflowAuthoringAgents() error = %v", err)
	}
	if len(capabilities) != 1 ||
		capabilities[0].ID != routing.DefaultAgentID ||
		!capabilities[0].IsDefault ||
		!reflect.DeepEqual(limits, []workflows.WorkflowAuthoringLimitCode{
			workflows.WorkflowAuthoringUnsafeFieldsOmitted,
		}) {
		t.Fatalf("noncanonical agent projection = %#v, limits %#v", capabilities, limits)
	}
}

func TestProjectWorkflowAuthoringToolsUsesDeterministicBoundedSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = false
	registry := tools.NewToolRegistry()
	for index := workflows.MaxWorkflowAuthoringTools * 2; index >= 0; index-- {
		registry.Register(&workflowAuthoringTestTool{
			name:       fmt.Sprintf("tool-%03d", index),
			parameters: map[string]any{},
		})
	}
	loop := workflowDependencyTestLoop(cfg)
	defer loop.Close()
	defaultAgent := &AgentInstance{Tools: registry}

	project := func() (
		[]workflows.WorkflowAuthoringToolCapability,
		[]workflows.WorkflowAuthoringLimitCode,
	) {
		t.Helper()
		projected, mcpTools, limits, err := loop.projectWorkflowAuthoringTools(
			context.Background(),
			defaultAgent,
			workflows.WorkflowAuthoringMCPDisabled,
			&workflows.WorkflowAuthoringShapeSanitizer{},
			nil,
		)
		if err != nil {
			t.Fatalf("projectWorkflowAuthoringTools() error = %v", err)
		}
		if len(mcpTools) != 0 {
			t.Fatalf("unexpected MCP tools: %#v", mcpTools)
		}
		return projected, workflows.NormalizeWorkflowAuthoringLimits(limits)
	}
	projected, limits := project()
	if len(projected) != workflows.MaxWorkflowAuthoringTools ||
		!reflect.DeepEqual(limits, []workflows.WorkflowAuthoringLimitCode{
			workflows.WorkflowAuthoringToolsTruncated,
		}) {
		t.Fatalf("bounded tools = %d, limits %#v", len(projected), limits)
	}
	for index, capability := range projected {
		want := fmt.Sprintf("tool-%03d", index)
		if capability.Name != want {
			t.Fatalf("tool %d = %q, want %q", index, capability.Name, want)
		}
	}
	again, againLimits := project()
	if !reflect.DeepEqual(again, projected) || !reflect.DeepEqual(againLimits, limits) {
		t.Fatal("bounded tool selection changed across map iterations")
	}
}

func TestProjectWorkflowAuthoringToolsRejectsOverboundMCPIdentityBeforeTarget(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	loop := workflowDependencyTestLoop(cfg)
	defer loop.Close()
	registry := tools.NewToolRegistry()
	registry.Register(tools.NewMCPTool(nil, "github", &sdkmcp.Tool{
		Name: strings.Repeat("x", 1<<20),
	}))

	projected, mcpTools, limits, err := loop.projectWorkflowAuthoringTools(
		context.Background(),
		&AgentInstance{Tools: registry},
		workflows.WorkflowAuthoringMCPReady,
		&workflows.WorkflowAuthoringShapeSanitizer{},
		nil,
	)
	if err != nil {
		t.Fatalf("projectWorkflowAuthoringTools() error = %v", err)
	}
	if len(projected) != 0 ||
		len(mcpTools) != 0 ||
		!reflect.DeepEqual(
			workflows.NormalizeWorkflowAuthoringLimits(limits),
			[]workflows.WorkflowAuthoringLimitCode{
				workflows.WorkflowAuthoringUnsafeFieldsOmitted,
			},
		) {
		t.Fatalf("overbound MCP projection = %#v %#v %#v", projected, mcpTools, limits)
	}
}

func TestWorkflowAuthoringBoundedSelectorsHonorCancellation(t *testing.T) {
	cfg := config.DefaultConfig()
	loop := workflowDependencyTestLoop(cfg)
	defer loop.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := loop.projectWorkflowAuthoringAgents(ctx, loop.registry, nil); err == nil {
		t.Fatal("agent selector ignored canceled context")
	}
	if _, _, _, err := loop.projectWorkflowAuthoringTools(
		ctx,
		loop.registry.GetDefaultAgent(),
		workflows.WorkflowAuthoringMCPDisabled,
		&workflows.WorkflowAuthoringShapeSanitizer{},
		nil,
	); err == nil {
		t.Fatal("tool selector ignored canceled context")
	}
}
