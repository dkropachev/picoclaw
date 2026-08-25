package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestMCPCatalogInvalidDiscoveryFailsBeforeNetworkAndPrompt(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		requests.Add(1)
		http.Error(writer, "MCP transport must not be reached", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	cfg.Tools.MCP.Discovery = config.ToolDiscoveryConfig{Enabled: true}
	loop := mcpCatalogRuntimeLoop(t, cfg)
	agents := mcpCatalogRuntimeAgents(t, loop)
	baselines := make(map[string]int, len(agents))
	for agentID, agent := range agents {
		baselines[agentID] = agent.Tools.Count()
		assertNoMCPCatalogPrompt(t, agent)
	}

	err := loop.ensureMCPInitializedForGeneration(
		context.Background(),
		cfg,
		loop.registry,
	)
	if err == nil || !strings.Contains(err.Error(), "neither 'use_bm25' nor 'use_regex'") {
		t.Fatalf("invalid discovery initialization error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid discovery reached MCP transport %d time(s)", requests.Load())
	}
	if loop.mcp.getManager() != nil {
		t.Fatal("invalid discovery retained an MCP manager")
	}
	for agentID, agent := range agents {
		if agent.Tools.Count() != baselines[agentID] {
			t.Fatalf(
				"agent %q tool count after invalid discovery = %d, want %d",
				agentID,
				agent.Tools.Count(),
				baselines[agentID],
			)
		}
		assertNoMCPCatalogPrompt(t, agent)
	}

	secondErr := loop.ensureMCPInitializedForGeneration(
		context.Background(),
		cfg,
		loop.registry,
	)
	if secondErr == nil || secondErr.Error() != err.Error() || requests.Load() != 0 {
		t.Fatalf(
			"cached invalid discovery = %v, requests=%d; want %v and no network",
			secondErr,
			requests.Load(),
			err,
		)
	}
}

func TestMCPCatalogCrossAgentCollisionRollsBackToolsPromptsAndManager(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"available", "search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	cfg.Tools.MCP.Discovery = config.ToolDiscoveryConfig{
		Enabled: true, UseBM25: true, UseRegex: true, TTL: 3, MaxSearchResults: 4,
	}
	deferred := true
	serverCfg := cfg.Tools.MCP.Servers["github"]
	serverCfg.Deferred = &deferred
	cfg.Tools.MCP.Servers["github"] = serverCfg
	loop := mcpCatalogRuntimeLoop(t, cfg)
	agents := mcpCatalogRuntimeAgents(t, loop)

	collisionName := mcp.CanonicalToolName("github", "search")
	collision := &allowlistTestTool{name: collisionName}
	agents["research"].Tools.Register(collision)
	mainBaseline := agents["main"].Tools.Count()
	researchBaseline := agents["research"].Tools.Count()

	err := loop.ensureMCPInitializedForGeneration(
		context.Background(),
		cfg,
		loop.registry,
	)
	if !errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
		t.Fatalf("cross-agent collision error = %v, want canonical collision", err)
	}
	if loop.mcp.getManager() != nil {
		t.Fatal("cross-agent collision retained the candidate MCP manager")
	}
	if agents["main"].Tools.Count() != mainBaseline ||
		agents["research"].Tools.Count() != researchBaseline {
		t.Fatalf(
			"cross-agent rollback counts = main:%d/%d research:%d/%d",
			agents["main"].Tools.Count(),
			mainBaseline,
			agents["research"].Tools.Count(),
			researchBaseline,
		)
	}
	for _, name := range []string{
		mcp.CanonicalToolName("github", "available"),
		tools.BM25SearchToolName,
		tools.RegexSearchToolName,
	} {
		if agents["main"].Tools.HasRegistered(name) {
			t.Fatalf("cross-agent rollback partially published main tool %q", name)
		}
	}
	registered, ok := agents["research"].Tools.GetRegistered(collisionName)
	if !ok || registered != collision {
		t.Fatalf("collision occupant = %T %p, want original %p", registered, registered, collision)
	}
	for _, agent := range agents {
		assertNoMCPCatalogPrompt(t, agent)
	}
}

func TestMCPCatalogSuccessfulGenerationIsExactFactoryBackedAndOwnerLocal(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"search", "status"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	cfg.Tools.MCP.Discovery = config.ToolDiscoveryConfig{
		Enabled: true, UseBM25: true, UseRegex: true, TTL: 3, MaxSearchResults: 4,
	}
	deferred := true
	serverCfg := cfg.Tools.MCP.Servers["github"]
	serverCfg.Deferred = &deferred
	cfg.Tools.MCP.Servers["github"] = serverCfg
	loop := mcpCatalogRuntimeLoop(t, cfg)

	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(),
		cfg,
		loop.registry,
	); err != nil {
		t.Fatal(err)
	}
	manager := loop.mcp.getManager()
	if manager == nil {
		t.Fatal("successful MCP catalog did not publish its manager")
	}

	agents := mcpCatalogRuntimeAgents(t, loop)
	remoteNames := []string{
		mcp.CanonicalToolName("github", "search"),
		mcp.CanonicalToolName("github", "status"),
	}
	allNames := append(append([]string(nil), remoteNames...),
		tools.BM25SearchToolName,
		tools.RegexSearchToolName,
	)
	var sourceSearch tools.Tool
	for agentID, agent := range agents {
		capabilities := make(map[string]tools.ToolInstantiationCapability)
		for _, capability := range agent.Tools.InstantiationCapabilities() {
			capabilities[capability.Name] = capability
		}
		for _, name := range allNames {
			capability, ok := capabilities[name]
			if !ok || !capability.FactoryBacked || capability.ImmutableShared {
				t.Fatalf("agent %q capability %q = %#v, %t", agentID, name, capability, ok)
			}
		}
		for _, name := range remoteNames {
			if _, callable := agent.Tools.Get(name); callable {
				t.Fatalf("agent %q deferred MCP tool %q is callable before discovery", agentID, name)
			}
		}
		for _, name := range []string{tools.BM25SearchToolName, tools.RegexSearchToolName} {
			if _, callable := agent.Tools.Get(name); !callable {
				t.Fatalf("agent %q discovery tool %q is not callable", agentID, name)
			}
		}
		prompt := mcpCatalogRuntimePrompt(agent, nil)
		if !strings.Contains(prompt, "MCP server `github` is connected") ||
			!strings.Contains(prompt, "It contributes 2 tool(s)") ||
			!strings.Contains(prompt, tools.BM25SearchToolName) ||
			!strings.Contains(prompt, tools.RegexSearchToolName) {
			t.Fatalf("agent %q successful MCP prompt = %q", agentID, prompt)
		}
		if agentID == "main" {
			sourceSearch, _ = agent.Tools.GetRegistered(remoteNames[0])
		}
	}

	main := agents["main"]
	baseDefinitions := main.Tools.ToProviderDefs()
	for _, surface := range []string{
		config.ToolSurfacePicoClaw,
		config.ToolSurfaceSimple,
		config.ToolSurfaceCodex,
	} {
		adapted := applyToolAdaptationSurface(surface, baseDefinitions)
		names := toolDefNamesForAdaptationTest(adapted)
		for _, discoveryName := range []string{
			tools.BM25SearchToolName,
			tools.RegexSearchToolName,
		} {
			if !slices.Contains(names, discoveryName) {
				t.Fatalf("%s adaptation omitted discovery tool %q: %v", surface, discoveryName, names)
			}
		}
	}
	if current := main.Tools.ToProviderDefs(); !reflect.DeepEqual(current, baseDefinitions) {
		t.Fatal("tool adaptation mutated the frozen MCP/discovery registry definitions")
	}
	compatibilityClone := main.Tools.Clone()
	cloneSearch, cloneOK := compatibilityClone.GetRegistered(remoteNames[0])
	if !cloneOK || cloneSearch != sourceSearch {
		t.Fatalf(
			"pre-P005c shallow clone MCP pointer = %T %p, want source %T %p",
			cloneSearch,
			cloneSearch,
			sourceSearch,
			sourceSearch,
		)
	}

	child, err := main.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "mcp-catalog-child"},
		[]string{remoteNames[0], tools.RegexSearchToolName},
	)
	if err != nil {
		t.Fatal(err)
	}
	childSearch, ok := child.GetRegistered(remoteNames[0])
	if !ok || childSearch == sourceSearch {
		t.Fatalf("owner MCP wrapper = %T %p, source %T %p", childSearch, childSearch, sourceSearch, sourceSearch)
	}
	if _, callable := child.Get(remoteNames[0]); callable {
		t.Fatal("owner hidden MCP wrapper started callable")
	}
	regex, ok := child.GetRegistered(tools.RegexSearchToolName)
	if !ok {
		t.Fatal("owner regex discovery wrapper missing")
	}
	discoveryResult := regex.Execute(context.Background(), map[string]any{
		"pattern": remoteNames[0],
	})
	if discoveryResult == nil || discoveryResult.IsError {
		t.Fatalf("owner discovery result = %#v", discoveryResult)
	}
	if _, callable := child.Get(remoteNames[0]); !callable {
		t.Fatal("owner discovery did not promote its hidden MCP wrapper")
	}
	if _, callable := main.Tools.Get(remoteNames[0]); callable {
		t.Fatal("owner discovery promoted the compatibility source registry")
	}
	if result := childSearch.Execute(context.Background(), map[string]any{}); result == nil || result.IsError {
		t.Fatalf("owner MCP execution = %#v", result)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if loop.mcp.getManager() != manager {
		t.Fatal("closing an owner registry retired the borrowed MCP manager")
	}
	if result := sourceSearch.Execute(context.Background(), map[string]any{}); result == nil || result.IsError {
		t.Fatalf("source MCP execution after owner close = %#v", result)
	}
}

func TestMCPCatalogInstallerBoundaryHidesToolsManagerAndPromptsUntilCommit(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	agents := mcpCatalogRuntimeAgents(t, loop)
	entered := make(chan struct{})
	release := make(chan struct{})
	installerCalls := atomic.Int64{}
	loop.mcp.installer = func(
		batches []tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		installerCalls.Add(1)
		close(entered)
		<-release
		return tools.InstallFactoryBackedTransaction(batches)
	}

	done := make(chan error, 1)
	go func() {
		done <- loop.ensureMCPInitializedForGeneration(
			context.Background(),
			cfg,
			loop.registry,
		)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP initialization did not reach the catalog installer")
	}
	if loop.mcp.getManager() != nil {
		t.Fatal("candidate manager was published before catalog commit")
	}
	remoteName := mcp.CanonicalToolName("github", "search")
	for _, agent := range agents {
		if agent.Tools.HasRegistered(remoteName) {
			t.Fatalf("candidate tool %q was visible while installer was blocked", remoteName)
		}
		assertNoMCPCatalogPrompt(t, agent)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MCP initialization did not finish after installer release")
	}
	if installerCalls.Load() != 1 || loop.mcp.getManager() == nil {
		t.Fatalf(
			"installer calls/manager = %d/%p, want one call and published manager",
			installerCalls.Load(),
			loop.mcp.getManager(),
		)
	}
	for _, agent := range agents {
		if !agent.Tools.HasRegistered(remoteName) {
			t.Fatalf("committed tool %q is missing after installer release", remoteName)
		}
		if !strings.Contains(mcpCatalogRuntimePrompt(agent, nil), "MCP server `github`") {
			t.Fatal("committed MCP prompt is missing after installer release")
		}
	}
}

func TestMCPCatalogInstallerFailureAndPanicRemainPrivateAndCached(t *testing.T) {
	for _, test := range []struct {
		name      string
		installer mcpFactoryBackedInstaller
		want      string
	}{
		{
			name: "error",
			installer: func([]tools.FactoryBackedBatch) ([]tools.FactoryBackedAdmission, error) {
				return nil, errors.New("injected installer failure")
			},
			want: "injected installer failure",
		},
		{
			name: "panic",
			installer: func([]tools.FactoryBackedBatch) ([]tools.FactoryBackedAdmission, error) {
				panic("injected installer panic")
			},
			want: "injected installer panic",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := workflowDependencyMCPTestServer(t, []string{"search"})
			cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
			loop := mcpCatalogRuntimeLoop(t, cfg)
			agents := mcpCatalogRuntimeAgents(t, loop)
			loop.mcp.installer = test.installer

			firstErr := loop.ensureMCPInitializedForGeneration(
				context.Background(), cfg, loop.registry,
			)
			if firstErr == nil || !strings.Contains(firstErr.Error(), test.want) {
				t.Fatalf("installer %s error = %v", test.name, firstErr)
			}
			if loop.mcp.getManager() != nil {
				t.Fatalf("installer %s retained a private manager", test.name)
			}
			remoteName := mcp.CanonicalToolName("github", "search")
			for _, agent := range agents {
				if agent.Tools.HasRegistered(remoteName) {
					t.Fatalf("installer %s partially published %q", test.name, remoteName)
				}
				assertNoMCPCatalogPrompt(t, agent)
			}

			secondErr := loop.ensureMCPInitializedForGeneration(
				context.Background(), cfg, loop.registry,
			)
			if secondErr == nil || secondErr.Error() != firstErr.Error() {
				t.Fatalf("installer %s cached error = %v, want %v", test.name, secondErr, firstErr)
			}
		})
	}
}

func TestMCPCatalogPostCommitProjectionFailureRetainsBorrowedManager(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	agents := mcpCatalogRuntimeAgents(t, loop)
	loop.mcp.installer = func(
		batches []tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		admissions, err := tools.InstallFactoryBackedTransaction(batches)
		if err != nil || len(admissions) == 0 {
			return admissions, err
		}
		// The real transaction has committed. Corrupt only the returned detached
		// projection to exercise the post-commit manager-retention boundary.
		return admissions[:len(admissions)-1], nil
	}

	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(), cfg, loop.registry,
	); err != nil {
		t.Fatalf("post-commit projection failure escaped as initialization error: %v", err)
	}
	manager := loop.mcp.getManager()
	if manager == nil {
		t.Fatal("post-commit projection failure did not retain the borrowed manager")
	}
	remoteName := mcp.CanonicalToolName("github", "search")
	for _, agent := range agents {
		wrapped, ok := agent.Tools.GetRegistered(remoteName)
		if !ok {
			t.Fatalf("post-commit tool %q is missing", remoteName)
		}
		if result := wrapped.Execute(context.Background(), map[string]any{}); result == nil || result.IsError {
			t.Fatalf("post-commit wrapper used a closed manager: %#v", result)
		}
		assertNoMCPCatalogPrompt(t, agent)
	}
}

func TestMCPCatalogCancellationDuringServerLoadPublishesNothing(t *testing.T) {
	server, listStarted, releaseList := blockingMCPListTestServer(t, "search")
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"github": server.URL})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	agents := mcpCatalogRuntimeAgents(t, loop)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- loop.ensureMCPInitializedForGeneration(ctx, cfg, loop.registry)
	}()
	select {
	case <-listStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP cancellation test did not reach tools/list")
	}
	cancel()
	releaseList()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cancel") {
			t.Fatalf("canceled MCP initialization error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled MCP initialization did not return")
	}
	if loop.mcp.getManager() != nil {
		t.Fatal("canceled MCP initialization retained a manager")
	}
	remoteName := mcp.CanonicalToolName("github", "search")
	for _, agent := range agents {
		if agent.Tools.HasRegistered(remoteName) {
			t.Fatalf("canceled MCP initialization published %q", remoteName)
		}
		assertNoMCPCatalogPrompt(t, agent)
	}
}

func TestMCPCatalogPartialServerLoadPublishesOnlyConnectedSurface(t *testing.T) {
	connected := workflowDependencyMCPTestServer(t, []string{"search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{
		"github": connected.URL,
		"broken": "http://127.0.0.1:1",
	})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(), cfg, loop.registry,
	); err != nil {
		t.Fatalf("partial MCP server load failed the whole generation: %v", err)
	}
	manager := loop.mcp.getManager()
	if manager == nil {
		t.Fatal("partial MCP server load did not publish its connected manager")
	}
	servers := manager.GetServers()
	if len(servers) != 1 || servers["github"] == nil || servers["broken"] != nil {
		t.Fatalf("partial manager servers = %#v, want connected github only", servers)
	}
	remoteName := mcp.CanonicalToolName("github", "search")
	for agentID, agent := range mcpCatalogRuntimeAgents(t, loop) {
		if !agent.Tools.HasRegistered(remoteName) {
			t.Fatalf("agent %q is missing connected partial-load tool %q", agentID, remoteName)
		}
		prompt := mcpCatalogRuntimePrompt(agent, nil)
		if !strings.Contains(prompt, "MCP server `github`") ||
			strings.Contains(prompt, "MCP server `broken`") {
			t.Fatalf("agent %q partial-load prompt = %q", agentID, prompt)
		}
	}
}

func TestMCPCatalogLossyServerPromptIdentitiesRemainExact(t *testing.T) {
	leftServer := workflowDependencyMCPTestServer(t, []string{"search"})
	rightServer := workflowDependencyMCPTestServer(t, []string{"search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{
		"a.b": leftServer.URL,
		"a/b": rightServer.URL,
	})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	if err := loop.ensureMCPInitializedForGeneration(
		context.Background(), cfg, loop.registry,
	); err != nil {
		t.Fatal(err)
	}
	leftName := mcp.CanonicalToolName("a.b", "search")
	rightName := mcp.CanonicalToolName("a/b", "search")
	if leftName == rightName {
		t.Fatal("lossy prompt test requires distinct canonical tool names")
	}
	for agentID, agent := range mcpCatalogRuntimeAgents(t, loop) {
		full := mcpCatalogRuntimePrompt(agent, nil)
		if !strings.Contains(full, "MCP server `a.b`") ||
			!strings.Contains(full, "MCP server `a/b`") {
			t.Fatalf("agent %q lost a lossy exact-server prompt: %q", agentID, full)
		}
		leftOnly := mcpCatalogRuntimePrompt(agent, []string{leftName})
		if !strings.Contains(leftOnly, "MCP server `a.b`") ||
			strings.Contains(leftOnly, "MCP server `a/b`") {
			t.Fatalf("agent %q left exact-tool prompt filter = %q", agentID, leftOnly)
		}
		rightOnly := mcpCatalogRuntimePrompt(agent, []string{rightName})
		if !strings.Contains(rightOnly, "MCP server `a/b`") ||
			strings.Contains(rightOnly, "MCP server `a.b`") {
			t.Fatalf("agent %q right exact-tool prompt filter = %q", agentID, rightOnly)
		}
	}
}

func mcpCatalogRuntimeConfig(
	t *testing.T,
	servers map[string]string,
) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Workspace: t.TempDir()},
		{ID: "research", Workspace: t.TempDir()},
	}
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers:    make(map[string]config.MCPServerConfig, len(servers)),
	}
	for name, url := range servers {
		cfg.Tools.MCP.Servers[name] = config.MCPServerConfig{
			Enabled: true,
			Type:    "http",
			URL:     url,
		}
	}
	return cfg
}

func mcpCatalogRuntimeLoop(t *testing.T, cfg *config.Config) *AgentLoop {
	t.Helper()
	loop := &AgentLoop{cfg: cfg, registry: NewAgentRegistry(cfg, nil)}
	t.Cleanup(loop.Close)
	return loop
}

func mcpCatalogRuntimeAgents(
	t *testing.T,
	loop *AgentLoop,
) map[string]*AgentInstance {
	t.Helper()
	result := make(map[string]*AgentInstance)
	for _, agentID := range []string{"main", "research"} {
		agent, ok := loop.registry.GetAgent(agentID)
		if !ok || agent == nil {
			t.Fatalf("MCP catalog test agent %q is unavailable", agentID)
		}
		result[agentID] = agent
	}
	return result
}

func mcpCatalogRuntimePrompt(agent *AgentInstance, allowedTools []string) string {
	if agent == nil || agent.ContextBuilder == nil {
		return ""
	}
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "test MCP catalog",
		AllowedTools:   append([]string(nil), allowedTools...),
	})
	if len(messages) == 0 {
		return ""
	}
	return messages[0].Content
}

func assertNoMCPCatalogPrompt(t *testing.T, agent *AgentInstance) {
	t.Helper()
	prompt := mcpCatalogRuntimePrompt(agent, nil)
	if strings.Contains(prompt, "MCP server `") ||
		strings.Contains(prompt, tools.BM25SearchToolName) ||
		strings.Contains(prompt, tools.RegexSearchToolName) {
		t.Fatalf("unexpected MCP catalog prompt before successful admission: %q", prompt)
	}
}
