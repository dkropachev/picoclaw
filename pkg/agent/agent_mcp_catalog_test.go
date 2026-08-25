package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type catalogMCPManager struct {
	mu    sync.Mutex
	calls []mcp.ToolIdentity
}

func (manager *catalogMCPManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	_ map[string]any,
) (*sdkmcp.CallToolResult, error) {
	manager.mu.Lock()
	manager.calls = append(manager.calls, mcp.ToolIdentity{
		Server: serverName,
		Tool:   toolName,
	})
	manager.mu.Unlock()
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "catalog result"},
	}}, nil
}

type catalogLegacyTool struct {
	name string
}

func (tool *catalogLegacyTool) Name() string { return tool.name }

func (*catalogLegacyTool) Description() string {
	return "catalog legacy occupant"
}

func (*catalogLegacyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*catalogLegacyTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("legacy")
}

func catalogAgent(
	t *testing.T,
	id string,
	registry *tools.ToolRegistry,
) *AgentInstance {
	t.Helper()
	return &AgentInstance{
		ID: id, Workspace: t.TempDir(), Tools: registry,
		ContextBuilder: NewContextBuilder(t.TempDir()),
	}
}

func catalogRegistry(
	t *testing.T,
	agents map[string]*AgentInstance,
) *AgentRegistry {
	t.Helper()
	return &AgentRegistry{agents: agents}
}

func catalogMCPConfig(
	discovery config.ToolDiscoveryConfig,
	servers map[string]config.MCPServerConfig,
) config.MCPConfig {
	return config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Discovery:  discovery,
		Servers:    servers,
	}
}

func catalogTarget(
	t *testing.T,
	id string,
	registry *tools.ToolRegistry,
) mcpCatalogAgent {
	t.Helper()
	agent := catalogAgent(t, id, registry)
	return mcpCatalogAgent{
		ID: id, Agent: agent, Registry: registry,
		Workspace: agent.Workspace,
	}
}

func TestResolveMCPDiscoverySettings(t *testing.T) {
	disabled, err := resolveMCPDiscoverySettings(config.ToolDiscoveryConfig{
		UseBM25: true, TTL: -1, MaxSearchResults: -2,
	})
	if err != nil || disabled != (effectiveMCPDiscoverySettings{}) {
		t.Fatalf("disabled settings = %#v, %v", disabled, err)
	}
	if got, resolveErr := resolveMCPDiscoverySettings(config.ToolDiscoveryConfig{
		Enabled: true,
	}); resolveErr == nil || got != (effectiveMCPDiscoverySettings{}) {
		t.Fatalf("missing matcher settings = %#v, %v", got, resolveErr)
	}
	defaults, err := resolveMCPDiscoverySettings(config.ToolDiscoveryConfig{
		Enabled: true, UseBM25: true, TTL: 0, MaxSearchResults: -1,
	})
	if err != nil || defaults != (effectiveMCPDiscoverySettings{
		Enabled: true, UseBM25: true,
		TTL:              defaultMCPDiscoveryTTL,
		MaxSearchResults: defaultMCPDiscoveryMaxSearchResults,
	}) {
		t.Fatalf("defaulted settings = %#v, %v", defaults, err)
	}
	exact, err := resolveMCPDiscoverySettings(config.ToolDiscoveryConfig{
		Enabled: true, UseBM25: true, UseRegex: true,
		TTL: 17, MaxSearchResults: 23,
	})
	if err != nil || exact != (effectiveMCPDiscoverySettings{
		Enabled: true, UseBM25: true, UseRegex: true,
		TTL: 17, MaxSearchResults: 23,
	}) {
		t.Fatalf("exact settings = %#v, %v", exact, err)
	}
}

func TestSnapshotMCPCatalogAgentsValidationAndOrder(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())
	alphaRegistry := tools.NewToolRegistry()
	zetaRegistry := tools.NewToolRegistry()
	valid := catalogRegistry(t, map[string]*AgentInstance{
		"zeta":  catalogAgent(t, "zeta", zetaRegistry),
		"alpha": catalogAgent(t, "alpha", alphaRegistry),
	})
	snapshot, err := snapshotMCPCatalogAgents(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 2 || snapshot[0].ID != "alpha" ||
		snapshot[0].Registry != alphaRegistry || snapshot[1].ID != "zeta" ||
		snapshot[1].Registry != zetaRegistry {
		t.Fatalf("agent snapshot = %#v", snapshot)
	}

	owned, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeRegistry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	shared := tools.NewToolRegistry()
	tests := []struct {
		name     string
		registry *AgentRegistry
	}{
		{name: "nil registry"},
		{name: "empty registry", registry: catalogRegistry(t, nil)},
		{name: "nil agent", registry: catalogRegistry(t, map[string]*AgentInstance{
			"main": nil,
		})},
		{name: "alias key missing normalized agent", registry: catalogRegistry(t, map[string]*AgentInstance{
			"MAIN": catalogAgent(t, "main", tools.NewToolRegistry()),
		})},
		{name: "nil tools", registry: catalogRegistry(t, map[string]*AgentInstance{
			"main": catalogAgent(t, "main", nil),
		})},
		{name: "nil context", registry: catalogRegistry(t, map[string]*AgentInstance{
			"main": {ID: "main", Tools: tools.NewToolRegistry()},
		})},
		{name: "owned tools", registry: catalogRegistry(t, map[string]*AgentInstance{
			"main": catalogAgent(t, "main", owned),
		})},
		{name: "aliased tools", registry: catalogRegistry(t, map[string]*AgentInstance{
			"alpha": catalogAgent(t, "alpha", shared),
			"beta":  catalogAgent(t, "beta", shared),
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, snapshotErr := snapshotMCPCatalogAgents(test.registry); snapshotErr == nil || got != nil {
				t.Fatalf("invalid snapshot = %#v, %v", got, snapshotErr)
			}
		})
	}
}

func TestStageMCPGenerationDeterministicDetachedBatches(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())
	deferred := true
	eager := false
	discovery, err := resolveMCPDiscoverySettings(config.ToolDiscoveryConfig{
		Enabled: true, UseBM25: true, UseRegex: true,
		TTL: 7, MaxSearchResults: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
	}
	toolC := &sdkmcp.Tool{
		Name: "c", Description: "original c", InputSchema: schema,
	}
	toolB := &sdkmcp.Tool{Name: "b", Description: "original b"}
	toolA := &sdkmcp.Tool{Name: "a", Description: "original a"}
	servers := map[string]*mcp.ServerConnection{
		"z_server": {Tools: []*sdkmcp.Tool{toolB, toolA}},
		"a_server": {Tools: []*sdkmcp.Tool{toolC}},
	}
	mcpCfg := catalogMCPConfig(config.ToolDiscoveryConfig{Enabled: true}, map[string]config.MCPServerConfig{
		"z_server": {Enabled: true, Deferred: &eager},
		"a_server": {Enabled: true, Deferred: &deferred},
	})
	alphaRegistry := tools.NewToolRegistry()
	betaRegistry := tools.NewToolRegistry()
	t.Cleanup(func() {
		_ = alphaRegistry.Close()
		_ = betaRegistry.Close()
	})
	alpha := catalogTarget(t, "alpha", alphaRegistry)
	beta := catalogTarget(t, "beta", betaRegistry)
	beta.Agent.MCPServerAllowlist = map[string]struct{}{"a_server": {}}
	manager := &catalogMCPManager{}
	stage, err := stageMCPGeneration(
		manager,
		servers,
		mcpCfg,
		discovery,
		[]mcpCatalogAgent{beta, alpha},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stage.UniqueTools != 3 || stage.AgentCount != 2 || len(stage.Batches) != 2 {
		t.Fatalf("stage counts = %#v", stage)
	}
	if stage.Batches[0].Registry != alphaRegistry || stage.Batches[1].Registry != betaRegistry {
		t.Fatalf("batch registry order = %p, %p", stage.Batches[0].Registry, stage.Batches[1].Registry)
	}
	wantNames := []string{
		mcp.CanonicalToolName("a_server", "c"),
		mcp.CanonicalToolName("z_server", "a"),
		mcp.CanonicalToolName("z_server", "b"),
		tools.BM25SearchToolName,
		tools.RegexSearchToolName,
		mcp.CanonicalToolName("a_server", "c"),
		tools.BM25SearchToolName,
		tools.RegexSearchToolName,
	}
	gotNames := make([]string, 0, len(stage.Sidecars))
	for index, sidecar := range stage.Sidecars {
		gotNames = append(gotNames, sidecar.Name)
		if sidecar.BatchIndex < 0 || sidecar.BatchIndex >= len(stage.Batches) ||
			sidecar.InstallIndex < 0 ||
			sidecar.InstallIndex >= len(stage.Batches[sidecar.BatchIndex].Installs) {
			t.Fatalf("sidecar %d has invalid indexes: %#v", index, sidecar)
		}
		install := stage.Batches[sidecar.BatchIndex].Installs[sidecar.InstallIndex]
		if install.Live.Name() != sidecar.Name {
			t.Fatalf("sidecar %d name = %q, live = %q", index, sidecar.Name, install.Live.Name())
		}
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("staged names = %#v, want %#v", gotNames, wantNames)
	}
	if !stage.Batches[0].Installs[0].Hidden ||
		stage.Batches[0].Installs[1].Hidden ||
		stage.Batches[0].Installs[3].Hidden {
		t.Fatal("staged hidden/core partition is incorrect")
	}

	// Mutating every SDK-facing alias after staging cannot change either agent's
	// detached wrapper or factory descriptor.
	toolC.Name = "mutated"
	toolC.Description = "mutated"
	toolC.InputSchema = map[string]any{"type": "array"}
	schema["type"] = "array"
	schema["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"
	for _, batchIndex := range []int{0, 1} {
		install := stage.Batches[batchIndex].Installs[0]
		if install.Live.Name() != mcp.CanonicalToolName("a_server", "c") ||
			install.Live.Description() != "[MCP:a_server] original c" {
			t.Fatalf("batch %d retained SDK aliases", batchIndex)
		}
		params := install.Live.Parameters()
		query := params["properties"].(map[string]any)["query"].(map[string]any)
		if params["type"] != "object" || query["type"] != "string" {
			t.Fatalf("batch %d frozen parameters = %#v", batchIndex, params)
		}
		if descriptor := install.Factory.Descriptor(); descriptor.Name != install.Live.Name() ||
			descriptor.Description != install.Live.Description() {
			t.Fatalf("batch %d descriptor = %#v", batchIndex, descriptor)
		}
	}

	admissions, err := tools.InstallFactoryBackedTransaction(stage.Batches)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := aggregateMCPAdmissions(stage, admissions)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRegistrations != 4 || len(summary.Agents) != 2 ||
		summary.Agents[0].AgentID != "alpha" ||
		summary.Agents[1].AgentID != "beta" {
		t.Fatalf("admission summary = %#v", summary)
	}
	for _, agent := range summary.Agents {
		if !agent.UseBM25 || !agent.UseRegex ||
			!slices.Equal(agent.DiscoveryToolNames, []string{
				tools.BM25SearchToolName,
				tools.RegexSearchToolName,
			}) {
			t.Fatalf("agent discovery summary = %#v", agent)
		}
	}
	if !slices.Equal(summary.Agents[0].Servers[1].ToolNames, []string{
		mcp.CanonicalToolName("z_server", "a"),
		mcp.CanonicalToolName("z_server", "b"),
	}) {
		t.Fatalf("exact admitted server names = %#v", summary.Agents[0].Servers)
	}
}

func TestStageMCPGenerationRejectsInvalidServerCatalogs(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())
	manager := &catalogMCPManager{}
	target := catalogTarget(t, "main", tools.NewToolRegistry())
	t.Cleanup(func() { _ = target.Registry.Close() })
	discovery := effectiveMCPDiscoverySettings{}
	validCfg := catalogMCPConfig(config.ToolDiscoveryConfig{}, map[string]config.MCPServerConfig{
		"server": {Enabled: true},
	})
	validServers := func(tool *sdkmcp.Tool) map[string]*mcp.ServerConnection {
		return map[string]*mcp.ServerConnection{
			"server": {Tools: []*sdkmcp.Tool{tool}},
		}
	}
	var typedNilManager *catalogMCPManager
	tests := []struct {
		name    string
		manager tools.MCPManager
		servers map[string]*mcp.ServerConnection
		cfg     config.MCPConfig
		want    error
	}{
		{
			name: "empty connected servers", manager: manager,
			servers: map[string]*mcp.ServerConnection{}, cfg: validCfg,
		},
		{
			name: "nil manager", servers: validServers(&sdkmcp.Tool{Name: "tool"}),
			cfg: validCfg,
		},
		{
			name: "typed nil manager", manager: typedNilManager,
			servers: validServers(&sdkmcp.Tool{Name: "tool"}), cfg: validCfg,
		},
		{
			name: "disabled generation", manager: manager,
			servers: validServers(&sdkmcp.Tool{Name: "tool"}),
			cfg:     config.MCPConfig{Servers: validCfg.Servers},
		},
		{
			name: "nil connection", manager: manager,
			servers: map[string]*mcp.ServerConnection{"server": nil}, cfg: validCfg,
		},
		{
			name: "missing config", manager: manager,
			servers: validServers(&sdkmcp.Tool{Name: "tool"}),
			cfg:     catalogMCPConfig(config.ToolDiscoveryConfig{}, nil),
		},
		{
			name: "disabled server", manager: manager,
			servers: validServers(&sdkmcp.Tool{Name: "tool"}),
			cfg: catalogMCPConfig(config.ToolDiscoveryConfig{}, map[string]config.MCPServerConfig{
				"server": {},
			}),
		},
		{
			name: "nil tool", manager: manager,
			servers: validServers(nil), cfg: validCfg,
		},
		{
			name: "malformed schema", manager: manager,
			servers: validServers(&sdkmcp.Tool{
				Name: "tool", InputSchema: json.RawMessage(`{"type":`),
			}),
			cfg: validCfg,
		},
		{
			name: "conflicting duplicate exact identity", manager: manager,
			servers: map[string]*mcp.ServerConnection{
				"server": {Tools: []*sdkmcp.Tool{
					{
						Name: "tool", Description: "same",
						InputSchema: map[string]any{"type": "object"},
					},
					{
						Name: "tool", Description: "same",
						InputSchema: map[string]any{"type": "array"},
					},
				}},
			},
			cfg: validCfg,
		},
		{
			name: "canonical collision", manager: manager,
			servers: map[string]*mcp.ServerConnection{
				"a":   {Tools: []*sdkmcp.Tool{{Name: "b_c"}}},
				"a_b": {Tools: []*sdkmcp.Tool{{Name: "c"}}},
			},
			cfg: catalogMCPConfig(config.ToolDiscoveryConfig{}, map[string]config.MCPServerConfig{
				"a": {Enabled: true}, "a_b": {Enabled: true},
			}),
			want: mcp.ErrCanonicalToolNameCollision,
		},
	}
	if _, err := stageMCPGeneration(
		manager,
		validServers(&sdkmcp.Tool{Name: "tool"}),
		validCfg,
		discovery,
		nil,
		nil,
	); err == nil {
		t.Fatal("empty catalog agent snapshot was accepted")
	}
	if _, err := stageMCPGeneration(
		manager,
		validServers(&sdkmcp.Tool{Name: "tool"}),
		validCfg,
		effectiveMCPDiscoverySettings{Enabled: true, UseBM25: true},
		[]mcpCatalogAgent{target},
		nil,
	); err == nil {
		t.Fatal("unnormalized effective discovery settings were accepted")
	}
	identicalDuplicates, err := stageMCPGeneration(
		manager,
		map[string]*mcp.ServerConnection{
			"server": {Tools: []*sdkmcp.Tool{
				{
					Name: "tool", Description: "same",
					InputSchema: map[string]any{"type": "object"},
				},
				{
					Name: "tool", Description: "same",
					InputSchema: map[string]any{"type": "object"},
				},
			}},
		},
		validCfg,
		discovery,
		[]mcpCatalogAgent{target},
		nil,
	)
	if err != nil || identicalDuplicates.UniqueTools != 1 ||
		len(identicalDuplicates.Batches) != 1 ||
		len(identicalDuplicates.Batches[0].Installs) != 1 {
		t.Fatalf("identical duplicate staging = %#v, %v", identicalDuplicates, err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := stageMCPGeneration(
				test.manager,
				test.servers,
				test.cfg,
				discovery,
				[]mcpCatalogAgent{target},
				nil,
			)
			if err == nil {
				t.Fatal("invalid catalog was accepted")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("catalog error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStageMCPGenerationRefreshAndAllowlistPolicy(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())
	deferred := true
	manager := &catalogMCPManager{}
	remote := &sdkmcp.Tool{Name: "search", Description: "search"}
	servers := map[string]*mcp.ServerConnection{
		"github": {Tools: []*sdkmcp.Tool{remote}},
	}
	mcpCfg := catalogMCPConfig(config.ToolDiscoveryConfig{Enabled: true}, map[string]config.MCPServerConfig{
		"github": {Enabled: true, Deferred: &deferred},
	})
	discovery := effectiveMCPDiscoverySettings{
		Enabled: true, UseBM25: true,
		TTL: 5, MaxSearchResults: 5,
	}

	t.Run("exact MCP and discovery refresh", func(t *testing.T) {
		registry := tools.NewToolRegistry()
		t.Cleanup(func() { _ = registry.Close() })
		oldMCP, oldMCPFactory, err := tools.NewMCPToolWithFactory(
			manager, "github", remote, "old", 17, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if registerErr := registry.RegisterFactoryBacked(
			oldMCP,
			oldMCPFactory,
		); registerErr != nil {
			t.Fatal(registerErr)
		}
		oldBM25 := tools.NewBM25SearchTool(registry, 2, 3)
		if registerErr := registry.RegisterFactoryBacked(
			oldBM25,
			tools.NewBM25SearchToolFactory(2, 3),
		); registerErr != nil {
			t.Fatal(registerErr)
		}
		target := catalogTarget(t, "main", registry)
		stage, err := stageMCPGeneration(
			manager, servers, mcpCfg, discovery,
			[]mcpCatalogAgent{target}, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(stage.Batches) != 1 || len(stage.Batches[0].Installs) != 2 ||
			stage.Batches[0].Installs[0].Expected != oldMCP ||
			stage.Batches[0].Installs[1].Expected != oldBM25 {
			t.Fatalf("refresh stage = %#v", stage)
		}
		admissions, err := tools.InstallFactoryBackedTransaction(stage.Batches)
		if err != nil {
			t.Fatal(err)
		}
		for _, admission := range admissions {
			if !admission.Admitted || !admission.Replaced {
				t.Fatalf("refresh admission = %#v", admission)
			}
		}
	})

	t.Run("wrong MCP occupant", func(t *testing.T) {
		registry := tools.NewToolRegistry()
		registry.Register(&catalogLegacyTool{
			name: mcp.CanonicalToolName("github", "search"),
		})
		target := catalogTarget(t, "main", registry)
		if _, err := stageMCPGeneration(
			manager, servers, mcpCfg, discovery,
			[]mcpCatalogAgent{target}, nil,
		); !errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
			t.Fatalf("wrong MCP occupant error = %v", err)
		}
	})

	t.Run("denied MCP occupant is ignored", func(t *testing.T) {
		registry := tools.NewToolRegistry()
		t.Cleanup(func() { _ = registry.Close() })
		name := mcp.CanonicalToolName("github", "search")
		legacy := &catalogLegacyTool{name: name}
		registry.Register(legacy)
		registry.SetAllowlist([]string{})
		target := catalogTarget(t, "main", registry)
		stage, err := stageMCPGeneration(
			manager, servers, mcpCfg, discovery,
			[]mcpCatalogAgent{target}, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if stage.Batches[0].Installs[0].Expected != nil {
			t.Fatal("denied MCP install inspected its existing occupant")
		}
		admissions, err := tools.InstallFactoryBackedTransaction(stage.Batches)
		if err != nil {
			t.Fatal(err)
		}
		current, occupied := registry.GetRegistered(name)
		if admissions[0].Admitted || admissions[0].Replaced ||
			!admissions[1].Admitted || !occupied || current != legacy {
			t.Fatalf("allowlist admissions = %#v", admissions)
		}
	})

	t.Run("wrong discovery occupant", func(t *testing.T) {
		registry := tools.NewToolRegistry()
		registry.Register(&catalogLegacyTool{name: tools.BM25SearchToolName})
		target := catalogTarget(t, "main", registry)
		if _, err := stageMCPGeneration(
			manager, servers, mcpCfg, discovery,
			[]mcpCatalogAgent{target}, nil,
		); err == nil || !strings.Contains(err.Error(), "discovery") {
			t.Fatalf("wrong discovery occupant error = %v", err)
		}
	})

	t.Run("wrong regex discovery occupant", func(t *testing.T) {
		registry := tools.NewToolRegistry()
		registry.Register(&catalogLegacyTool{name: tools.RegexSearchToolName})
		target := catalogTarget(t, "main", registry)
		regexDiscovery := discovery
		regexDiscovery.UseBM25 = false
		regexDiscovery.UseRegex = true
		if _, err := stageMCPGeneration(
			manager, servers, mcpCfg, regexDiscovery,
			[]mcpCatalogAgent{target}, nil,
		); err == nil || !strings.Contains(err.Error(), "discovery") {
			t.Fatalf("wrong regex discovery occupant error = %v", err)
		}
	})
}

func TestMCPCatalogHelperBoundaryErrors(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())
	manager := &catalogMCPManager{}

	blankIDRegistry := catalogRegistry(t, map[string]*AgentInstance{
		"": catalogAgent(t, "", tools.NewToolRegistry()),
	})
	if _, err := snapshotMCPCatalogAgents(blankIDRegistry); err == nil {
		t.Fatal("blank agent ID was accepted")
	}
	validCfg := catalogMCPConfig(config.ToolDiscoveryConfig{}, map[string]config.MCPServerConfig{
		"server": {Enabled: true},
	})
	if _, _, err := snapshotMCPServerSpecs(
		manager,
		map[string]*mcp.ServerConnection{" ": {Tools: []*sdkmcp.Tool{}}},
		catalogMCPConfig(config.ToolDiscoveryConfig{}, map[string]config.MCPServerConfig{
			" ": {Enabled: true},
		}),
		effectiveMCPDiscoverySettings{},
		nil,
	); err == nil {
		t.Fatal("inexact server name was accepted")
	}

	validTarget := catalogTarget(t, "main", tools.NewToolRegistry())
	t.Cleanup(func() { _ = validTarget.Registry.Close() })
	serverWithoutTools := map[string]*mcp.ServerConnection{
		"server": {Tools: []*sdkmcp.Tool{}},
	}
	type targetCase struct {
		name   string
		agents []mcpCatalogAgent
	}
	tests := make([]targetCase, 0, 4)
	tests = append(tests,
		targetCase{name: "invalid target", agents: []mcpCatalogAgent{{ID: "main"}}},
		targetCase{name: "duplicate target ID", agents: []mcpCatalogAgent{validTarget, validTarget}},
		targetCase{name: "aliased target registry", agents: []mcpCatalogAgent{
			validTarget,
			{
				ID: "other", Agent: &AgentInstance{
					ID: "other", Tools: validTarget.Registry,
					ContextBuilder: NewContextBuilder(t.TempDir()),
				},
				Registry: validTarget.Registry,
			},
		}},
	)
	owned, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeRegistry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	tests = append(tests, targetCase{
		name:   "owned target",
		agents: []mcpCatalogAgent{catalogTarget(t, "owned", owned)},
	})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, stageErr := stageMCPGeneration(
				manager,
				serverWithoutTools,
				validCfg,
				effectiveMCPDiscoverySettings{},
				test.agents,
				nil,
			); stageErr == nil {
				t.Fatal("invalid catalog target was accepted")
			}
		})
	}
	noInstalls, err := stageMCPGeneration(
		manager,
		serverWithoutTools,
		validCfg,
		effectiveMCPDiscoverySettings{},
		[]mcpCatalogAgent{validTarget},
		nil,
	)
	if err != nil || len(noInstalls.Batches) != 0 || len(noInstalls.Sidecars) != 0 {
		t.Fatalf("empty connected tool catalog = %#v, %v", noInstalls, err)
	}

	if _, refreshErr := expectedMCPRefresh(
		nil,
		"name",
		"server",
		"tool",
	); refreshErr == nil {
		t.Fatal("nil MCP refresh registry was accepted")
	}
	refreshRegistry := tools.NewToolRegistry()
	requestedName := mcp.CanonicalToolName("server", "tool")
	// Register the wrong logical identity under the requested canonical key via
	// a compatibility legacy alias to exercise the exact-identity collision.
	refreshRegistry.Register(&catalogLegacyTool{name: requestedName})
	if _, refreshErr := expectedMCPRefresh(
		refreshRegistry,
		requestedName,
		"server",
		"tool",
	); !errors.Is(refreshErr, mcp.ErrCanonicalToolNameCollision) {
		t.Fatalf("legacy refresh collision = %v", refreshErr)
	}

	exactMCPRegistry := tools.NewToolRegistry()
	existing, existingFactory, err := tools.NewMCPToolWithFactory(
		manager,
		"other",
		&sdkmcp.Tool{Name: "tool"},
		"",
		0,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := exactMCPRegistry.RegisterFactoryBacked(
		existing,
		existingFactory,
	); registerErr != nil {
		t.Fatal(registerErr)
	}
	if _, refreshErr := expectedMCPRefresh(
		exactMCPRegistry,
		existing.Name(),
		"different",
		"tool",
	); !errors.Is(refreshErr, mcp.ErrCanonicalToolNameCollision) {
		t.Fatalf("exact wrapper wrong-identity collision = %v", refreshErr)
	}
	t.Cleanup(func() { _ = exactMCPRegistry.Close() })

	if _, refreshErr := expectedDiscoveryRefresh(
		nil,
		tools.RegexSearchToolName,
		mcpInstallDiscoveryRegex,
	); refreshErr == nil {
		t.Fatal("nil discovery refresh registry was accepted")
	}
	discoveryRegistry := tools.NewToolRegistry()
	regex := tools.NewRegexSearchTool(discoveryRegistry, 2, 3)
	if registerErr := discoveryRegistry.RegisterFactoryBacked(
		regex,
		tools.NewRegexSearchToolFactory(2, 3),
	); registerErr != nil {
		t.Fatal(registerErr)
	}
	if got, refreshErr := expectedDiscoveryRefresh(
		discoveryRegistry,
		tools.RegexSearchToolName,
		mcpInstallDiscoveryRegex,
	); refreshErr != nil || got != regex {
		t.Fatalf("regex refresh = %T, %v", got, refreshErr)
	}
	if _, refreshErr := expectedDiscoveryRefresh(
		discoveryRegistry,
		tools.RegexSearchToolName,
		mcpInstallKind(99),
	); refreshErr == nil {
		t.Fatal("unknown discovery refresh kind was accepted")
	}
	t.Cleanup(func() { _ = discoveryRegistry.Close() })

	emptyCapSource := tools.NewToolRegistry()
	emptyCap, err := emptyCapSource.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "empty-cap"},
		[]string{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, refreshErr := expectedDiscoveryRefresh(
		emptyCap,
		tools.RegexSearchToolName,
		mcpInstallDiscoveryRegex,
	); refreshErr != nil || got != nil {
		t.Fatalf("denied discovery refresh = %T, %v", got, refreshErr)
	}
	_ = emptyCap.Close()
}

func TestAggregateMCPAdmissionsExactNamesAndMalformedVectors(t *testing.T) {
	stage := stagedMCPGeneration{Sidecars: []mcpInstallSidecar{
		{
			BatchIndex: 0, InstallIndex: 0, Name: "mcp_z_b",
			AgentID: "zeta", ServerName: "z", Kind: mcpInstallRemote,
			Deferred: true,
		},
		{
			BatchIndex: 0, InstallIndex: 1, Name: "mcp_z_a",
			AgentID: "zeta", ServerName: "z", Kind: mcpInstallRemote,
			Deferred: true,
		},
		{
			BatchIndex: 0, InstallIndex: 2, Name: tools.RegexSearchToolName,
			AgentID: "zeta", Kind: mcpInstallDiscoveryRegex,
		},
		{
			BatchIndex: 1, InstallIndex: 0, Name: "mcp_a_tool",
			AgentID: "alpha", ServerName: "a", Kind: mcpInstallRemote,
		},
		{
			BatchIndex: 1, InstallIndex: 1, Name: tools.BM25SearchToolName,
			AgentID: "alpha", Kind: mcpInstallDiscoveryBM25,
		},
	}}
	valid := []tools.FactoryBackedAdmission{
		{BatchIndex: 0, InstallIndex: 0, Name: "mcp_z_b", Admitted: true},
		{BatchIndex: 0, InstallIndex: 1, Name: "mcp_z_a", Admitted: true, Replaced: true},
		{BatchIndex: 0, InstallIndex: 2, Name: tools.RegexSearchToolName, Admitted: true},
		{BatchIndex: 1, InstallIndex: 0, Name: "mcp_a_tool", Admitted: true},
		{BatchIndex: 1, InstallIndex: 1, Name: tools.BM25SearchToolName, Admitted: true},
	}
	summary, err := aggregateMCPAdmissions(stage, valid)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRegistrations != 3 || len(summary.Agents) != 2 ||
		summary.Agents[0].AgentID != "alpha" ||
		summary.Agents[1].AgentID != "zeta" ||
		!slices.Equal(summary.Agents[1].Servers[0].ToolNames, []string{
			"mcp_z_a", "mcp_z_b",
		}) ||
		summary.Agents[0].UseBM25 || summary.Agents[0].UseRegex ||
		len(summary.Agents[0].DiscoveryToolNames) != 0 ||
		!slices.Equal(summary.Agents[1].DiscoveryToolNames, []string{
			tools.RegexSearchToolName,
		}) {
		t.Fatalf("admission summary = %#v", summary)
	}
	denied := append([]tools.FactoryBackedAdmission(nil), valid...)
	denied[0].Admitted = false
	denied[0].Replaced = false
	deniedSummary, err := aggregateMCPAdmissions(stage, denied)
	if err != nil || deniedSummary.TotalRegistrations != 2 ||
		slices.Contains(deniedSummary.Agents[1].Servers[0].ToolNames, "mcp_z_b") {
		t.Fatalf("ordinary denied admission summary = %#v, %v", deniedSummary, err)
	}

	tests := []struct {
		name       string
		stage      stagedMCPGeneration
		admissions []tools.FactoryBackedAdmission
	}{
		{name: "count", stage: stage, admissions: valid[:len(valid)-1]},
		{name: "batch index", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), valid...)
			cloned[0].BatchIndex = 9
			return cloned
		}()},
		{name: "install index", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), valid...)
			cloned[0].InstallIndex = 9
			return cloned
		}()},
		{name: "name", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), valid...)
			cloned[0].Name = "wrong"
			return cloned
		}()},
		{name: "denied replacement", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), valid...)
			cloned[0].Admitted = false
			cloned[0].Replaced = true
			return cloned
		}()},
		{name: "unknown kind", stage: stagedMCPGeneration{Sidecars: []mcpInstallSidecar{{
			BatchIndex: 0, InstallIndex: 0, Name: "unknown",
			AgentID: "main", Kind: mcpInstallKind(99),
		}}}, admissions: []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "unknown", Admitted: true,
		}}},
		{name: "blank agent", stage: stagedMCPGeneration{Sidecars: []mcpInstallSidecar{{
			BatchIndex: 0, InstallIndex: 0, Name: "mcp_a",
			Kind: mcpInstallRemote, ServerName: "server",
		}}}, admissions: []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "mcp_a", Admitted: true,
		}}},
		{name: "missing server", stage: stagedMCPGeneration{Sidecars: []mcpInstallSidecar{{
			BatchIndex: 0, InstallIndex: 0, Name: "mcp_a",
			AgentID: "main", Kind: mcpInstallRemote,
		}}}, admissions: []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "mcp_a", Admitted: true,
		}}},
		{name: "inconsistent deferred", stage: stagedMCPGeneration{Sidecars: []mcpInstallSidecar{
			{
				BatchIndex: 0, InstallIndex: 0, Name: "mcp_a",
				AgentID: "main", ServerName: "server", Kind: mcpInstallRemote,
			},
			{
				BatchIndex: 0, InstallIndex: 1, Name: "mcp_b",
				AgentID: "main", ServerName: "server", Kind: mcpInstallRemote,
				Deferred: true,
			},
		}}, admissions: []tools.FactoryBackedAdmission{
			{BatchIndex: 0, InstallIndex: 0, Name: "mcp_a", Admitted: true},
			{BatchIndex: 0, InstallIndex: 1, Name: "mcp_b", Admitted: true},
		}},
		{name: "duplicate admitted name", stage: stagedMCPGeneration{Sidecars: []mcpInstallSidecar{
			{
				BatchIndex: 0, InstallIndex: 0, Name: "mcp_same",
				AgentID: "main", ServerName: "server", Kind: mcpInstallRemote,
			},
			{
				BatchIndex: 0, InstallIndex: 1, Name: "mcp_same",
				AgentID: "main", ServerName: "server", Kind: mcpInstallRemote,
			},
		}}, admissions: []tools.FactoryBackedAdmission{
			{BatchIndex: 0, InstallIndex: 0, Name: "mcp_same", Admitted: true},
			{BatchIndex: 0, InstallIndex: 1, Name: "mcp_same", Admitted: true},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, aggregateErr := aggregateMCPAdmissions(
				test.stage,
				test.admissions,
			); aggregateErr == nil || !reflect.DeepEqual(got, admittedMCPGeneration{}) {
				t.Fatalf("malformed aggregation = %#v, %v", got, aggregateErr)
			}
		})
	}
}
