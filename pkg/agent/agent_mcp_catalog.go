package agent

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	defaultMCPDiscoveryTTL              = 5
	defaultMCPDiscoveryMaxSearchResults = 5
)

type effectiveMCPDiscoverySettings struct {
	Enabled          bool
	UseBM25          bool
	UseRegex         bool
	TTL              int
	MaxSearchResults int
}

type mcpCatalogAgent struct {
	ID        string
	Agent     *AgentInstance
	Registry  *tools.ToolRegistry
	Workspace string
}

type mcpInstallKind uint8

const (
	mcpInstallRemote mcpInstallKind = iota
	mcpInstallDiscoveryBM25
	mcpInstallDiscoveryRegex
)

type mcpInstallSidecar struct {
	BatchIndex   int
	InstallIndex int
	Name         string
	AgentID      string
	ServerName   string
	Kind         mcpInstallKind
	Deferred     bool
}

type stagedMCPGeneration struct {
	Batches     []tools.FactoryBackedBatch
	Sidecars    []mcpInstallSidecar
	UniqueTools int
	AgentCount  int
}

type admittedMCPServer struct {
	Name      string
	ToolCount int
	ToolNames []string
	Deferred  bool
}

type admittedMCPAgent struct {
	AgentID            string
	Servers            []admittedMCPServer
	DiscoveryToolNames []string
	UseBM25            bool
	UseRegex           bool
}

type admittedMCPGeneration struct {
	Agents             []admittedMCPAgent
	TotalRegistrations int
}

type mcpFactoryBackedInstaller func(
	[]tools.FactoryBackedBatch,
) ([]tools.FactoryBackedAdmission, error)

// frozenMCPRemoteSpec is the only form retained after reading one mutable SDK
// declaration. Parent-package factories are then built from this detached
// spec, so separate agents cannot observe different SDK mutations.
type frozenMCPRemoteSpec struct {
	serverName       string
	toolName         string
	description      string
	canonicalName    string
	finalDescription string
	parameters       map[string]any
}

type frozenMCPServerSpec struct {
	name     string
	deferred bool
	tools    []frozenMCPRemoteSpec
}

func resolveMCPDiscoverySettings(
	raw config.ToolDiscoveryConfig,
) (effectiveMCPDiscoverySettings, error) {
	if !raw.Enabled {
		return effectiveMCPDiscoverySettings{}, nil
	}
	if !raw.UseBM25 && !raw.UseRegex {
		return effectiveMCPDiscoverySettings{}, fmt.Errorf(
			"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' " +
				"is set to true in the configuration",
		)
	}
	ttl := raw.TTL
	if ttl <= 0 {
		ttl = defaultMCPDiscoveryTTL
	}
	maxSearchResults := raw.MaxSearchResults
	if maxSearchResults <= 0 {
		maxSearchResults = defaultMCPDiscoveryMaxSearchResults
	}
	return effectiveMCPDiscoverySettings{
		Enabled: true, UseBM25: raw.UseBM25, UseRegex: raw.UseRegex,
		TTL: ttl, MaxSearchResults: maxSearchResults,
	}, nil
}

func snapshotMCPCatalogAgents(registry *AgentRegistry) ([]mcpCatalogAgent, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP agent registry is nil")
	}
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	if len(agentIDs) == 0 {
		return nil, fmt.Errorf("MCP agent registry is empty")
	}
	result := make([]mcpCatalogAgent, 0, len(agentIDs))
	seenRegistries := make(map[*tools.ToolRegistry]string, len(agentIDs))
	for _, agentID := range agentIDs {
		if strings.TrimSpace(agentID) == "" {
			return nil, fmt.Errorf("MCP agent ID must be non-empty")
		}
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			return nil, fmt.Errorf("MCP agent %q is unavailable", agentID)
		}
		if agent.Tools == nil {
			return nil, fmt.Errorf("MCP agent %q tool registry is nil", agentID)
		}
		if agent.ContextBuilder == nil {
			return nil, fmt.Errorf("MCP agent %q context builder is nil", agentID)
		}
		if _, owned := agent.Tools.Owner(); owned {
			return nil, fmt.Errorf(
				"MCP agent %q requires a compatibility tool registry",
				agentID,
			)
		}
		if previous, duplicate := seenRegistries[agent.Tools]; duplicate {
			return nil, fmt.Errorf(
				"MCP agents %q and %q alias one tool registry",
				previous,
				agentID,
			)
		}
		seenRegistries[agent.Tools] = agentID
		result = append(result, mcpCatalogAgent{
			ID: agentID, Agent: agent, Registry: agent.Tools,
			Workspace: agent.Workspace,
		})
	}
	return result, nil
}

func snapshotMCPServerSpecs(
	manager tools.MCPManager,
	servers map[string]*mcp.ServerConnection,
	mcpCfg config.MCPConfig,
	discovery effectiveMCPDiscoverySettings,
	publisher runtimeevents.Bus,
) ([]frozenMCPServerSpec, int, error) {
	if manager == nil || reflect.ValueOf(manager).Kind() == reflect.Pointer &&
		reflect.ValueOf(manager).IsNil() {
		return nil, 0, fmt.Errorf("MCP manager is nil")
	}
	if !mcpCfg.Enabled {
		return nil, 0, fmt.Errorf("MCP generation config is disabled")
	}
	if len(servers) == 0 {
		return nil, 0, fmt.Errorf("MCP connected server snapshot is empty")
	}
	serverNames := make([]string, 0, len(servers))
	for serverName := range servers {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	result := make([]frozenMCPServerSpec, 0, len(serverNames))
	identities := make([]mcp.ToolIdentity, 0)
	uniqueTools := 0

	for _, serverName := range serverNames {
		if serverName == "" || serverName != strings.TrimSpace(serverName) {
			return nil, 0, fmt.Errorf("MCP server name must be exact and non-empty")
		}
		connection := servers[serverName]
		if connection == nil {
			return nil, 0, fmt.Errorf("MCP server %q connection is nil", serverName)
		}
		serverCfg, configured := mcpCfg.Servers[serverName]
		if !configured || !serverCfg.Enabled {
			return nil, 0, fmt.Errorf(
				"connected MCP server %q has no enabled generation config",
				serverName,
			)
		}
		spec := frozenMCPServerSpec{
			name: serverName,
			deferred: serverIsDeferred(
				discovery.Enabled,
				serverCfg,
			),
			tools: make([]frozenMCPRemoteSpec, 0, len(connection.Tools)),
		}
		for _, remote := range connection.Tools {
			frozen, err := snapshotMCPRemoteSpec(
				manager,
				serverName,
				remote,
				mcpCfg.GetMaxInlineTextChars(),
				publisher,
			)
			if err != nil {
				return nil, 0, err
			}
			spec.tools = append(spec.tools, frozen)
		}
		sort.Slice(spec.tools, func(left, right int) bool {
			if spec.tools[left].toolName != spec.tools[right].toolName {
				return spec.tools[left].toolName < spec.tools[right].toolName
			}
			return spec.tools[left].canonicalName < spec.tools[right].canonicalName
		})
		deduplicated := make([]frozenMCPRemoteSpec, 0, len(spec.tools))
		for _, remote := range spec.tools {
			if len(deduplicated) > 0 &&
				deduplicated[len(deduplicated)-1].toolName == remote.toolName {
				if !sameFrozenMCPRemoteSpec(
					deduplicated[len(deduplicated)-1],
					remote,
				) {
					return nil, 0, fmt.Errorf(
						"MCP server %q repeats tool identity %q with conflicting frozen metadata",
						serverName,
						remote.toolName,
					)
				}
				continue
			}
			deduplicated = append(deduplicated, remote)
			identities = append(identities, mcp.ToolIdentity{
				Server: serverName,
				Tool:   remote.toolName,
			})
		}
		spec.tools = deduplicated
		uniqueTools += len(spec.tools)
		result = append(result, spec)
	}
	if err := mcp.DetectCanonicalToolNameCollision(identities); err != nil {
		return nil, 0, err
	}
	return result, uniqueTools, nil
}

func sameFrozenMCPRemoteSpec(left, right frozenMCPRemoteSpec) bool {
	return left.serverName == right.serverName &&
		left.toolName == right.toolName &&
		left.canonicalName == right.canonicalName &&
		left.finalDescription == right.finalDescription &&
		reflect.DeepEqual(left.parameters, right.parameters)
}

func snapshotMCPRemoteSpec(
	manager tools.MCPManager,
	serverName string,
	remote *sdkmcp.Tool,
	maxInlineTextRunes int,
	publisher runtimeevents.Bus,
) (frozenMCPRemoteSpec, error) {
	if remote == nil {
		return frozenMCPRemoteSpec{}, fmt.Errorf(
			"MCP server %q contains a nil SDK tool",
			serverName,
		)
	}
	// Read each mutable SDK field once, then validate only the detached shell.
	detachedRemote := &sdkmcp.Tool{
		Name:        remote.Name,
		Description: remote.Description,
		InputSchema: remote.InputSchema,
	}
	probe, _, err := tools.NewMCPToolWithFactory(
		manager,
		serverName,
		detachedRemote,
		"",
		maxInlineTextRunes,
		publisher,
	)
	if err != nil {
		return frozenMCPRemoteSpec{}, err
	}
	serverIdentity, toolIdentity := probe.MCPIdentity()
	if serverIdentity != serverName || toolIdentity != detachedRemote.Name {
		return frozenMCPRemoteSpec{}, fmt.Errorf(
			"MCP tool %q/%q snapshot identity changed",
			serverName,
			detachedRemote.Name,
		)
	}
	return frozenMCPRemoteSpec{
		serverName: serverName, toolName: detachedRemote.Name,
		description: detachedRemote.Description, canonicalName: probe.Name(),
		finalDescription: probe.Description(), parameters: probe.Parameters(),
	}, nil
}

func (spec frozenMCPRemoteSpec) buildForAgent(
	manager tools.MCPManager,
	workspace string,
	maxInlineTextRunes int,
	publisher runtimeevents.Bus,
) (*tools.MCPTool, tools.ToolFactory, error) {
	return tools.NewMCPToolWithFactory(
		manager,
		spec.serverName,
		&sdkmcp.Tool{
			Name: spec.toolName, Description: spec.description,
			InputSchema: spec.parameters,
		},
		workspace,
		maxInlineTextRunes,
		publisher,
	)
}

func stageMCPGeneration(
	manager tools.MCPManager,
	servers map[string]*mcp.ServerConnection,
	mcpCfg config.MCPConfig,
	discovery effectiveMCPDiscoverySettings,
	agents []mcpCatalogAgent,
	publisher runtimeevents.Bus,
) (stagedMCPGeneration, error) {
	if len(agents) == 0 {
		return stagedMCPGeneration{}, fmt.Errorf("MCP catalog agent snapshot is empty")
	}
	if discovery.Enabled && (!discovery.UseBM25 && !discovery.UseRegex ||
		discovery.TTL <= 0 || discovery.MaxSearchResults <= 0) {
		return stagedMCPGeneration{}, fmt.Errorf(
			"effective MCP discovery settings are invalid",
		)
	}
	serverSpecs, uniqueTools, err := snapshotMCPServerSpecs(
		manager,
		servers,
		mcpCfg,
		discovery,
		publisher,
	)
	if err != nil {
		return stagedMCPGeneration{}, err
	}

	orderedAgents := append([]mcpCatalogAgent(nil), agents...)
	sort.Slice(orderedAgents, func(left, right int) bool {
		return orderedAgents[left].ID < orderedAgents[right].ID
	})
	seenAgentIDs := make(map[string]struct{}, len(orderedAgents))
	seenRegistries := make(map[*tools.ToolRegistry]string, len(orderedAgents))
	for _, agent := range orderedAgents {
		if agent.ID == "" || agent.ID != strings.TrimSpace(agent.ID) ||
			agent.Agent == nil || agent.Registry == nil || agent.Agent.Tools != agent.Registry ||
			agent.Agent.ContextBuilder == nil {
			return stagedMCPGeneration{}, fmt.Errorf(
				"MCP catalog agent %q is invalid",
				agent.ID,
			)
		}
		if _, owned := agent.Registry.Owner(); owned {
			return stagedMCPGeneration{}, fmt.Errorf(
				"MCP catalog agent %q requires a compatibility tool registry",
				agent.ID,
			)
		}
		if _, duplicate := seenAgentIDs[agent.ID]; duplicate {
			return stagedMCPGeneration{}, fmt.Errorf(
				"MCP catalog agent %q is duplicated",
				agent.ID,
			)
		}
		seenAgentIDs[agent.ID] = struct{}{}
		if previous, duplicate := seenRegistries[agent.Registry]; duplicate {
			return stagedMCPGeneration{}, fmt.Errorf(
				"MCP catalog agents %q and %q alias one tool registry",
				previous,
				agent.ID,
			)
		}
		seenRegistries[agent.Registry] = agent.ID
	}

	var bm25Factory, regexFactory tools.ToolFactory
	if discovery.Enabled && discovery.UseBM25 {
		bm25Factory = tools.NewBM25SearchToolFactory(
			discovery.TTL,
			discovery.MaxSearchResults,
		)
	}
	if discovery.Enabled && discovery.UseRegex {
		regexFactory = tools.NewRegexSearchToolFactory(
			discovery.TTL,
			discovery.MaxSearchResults,
		)
	}

	stage := stagedMCPGeneration{
		Batches:     make([]tools.FactoryBackedBatch, 0, len(orderedAgents)),
		Sidecars:    make([]mcpInstallSidecar, 0),
		UniqueTools: uniqueTools, AgentCount: len(orderedAgents),
	}
	for _, target := range orderedAgents {
		batch := tools.FactoryBackedBatch{Registry: target.Registry}
		batchSidecars := make([]mcpInstallSidecar, 0)
		hasDiscoverableServer := false
		for _, server := range serverSpecs {
			if !target.Agent.AllowsMCPServer(server.name) {
				continue
			}
			if discovery.Enabled && server.deferred && len(server.tools) > 0 {
				hasDiscoverableServer = true
			}
			for _, remote := range server.tools {
				live, factory, buildErr := remote.buildForAgent(
					manager,
					target.Workspace,
					mcpCfg.GetMaxInlineTextChars(),
					publisher,
				)
				if buildErr != nil {
					return stagedMCPGeneration{}, buildErr
				}
				expected, expectedErr := expectedMCPRefresh(
					target.Registry,
					live.Name(),
					server.name,
					remote.toolName,
				)
				if expectedErr != nil {
					return stagedMCPGeneration{}, fmt.Errorf(
						"agent %q: %w",
						target.ID,
						expectedErr,
					)
				}
				batch.Installs = append(batch.Installs, tools.FactoryBackedInstall{
					Live: live, Factory: factory,
					Hidden: server.deferred, Expected: expected,
				})
				batchSidecars = append(batchSidecars, mcpInstallSidecar{
					Name: live.Name(), AgentID: target.ID,
					ServerName: server.name, Kind: mcpInstallRemote,
					Deferred: server.deferred,
				})
			}
		}

		if hasDiscoverableServer && bm25Factory != nil {
			live := tools.NewBM25SearchTool(
				target.Registry,
				discovery.TTL,
				discovery.MaxSearchResults,
			)
			expected, expectedErr := expectedDiscoveryRefresh(
				target.Registry,
				tools.BM25SearchToolName,
				mcpInstallDiscoveryBM25,
			)
			if expectedErr != nil {
				return stagedMCPGeneration{}, fmt.Errorf(
					"agent %q: %w",
					target.ID,
					expectedErr,
				)
			}
			batch.Installs = append(batch.Installs, tools.FactoryBackedInstall{
				Live: live, Factory: bm25Factory, Expected: expected,
			})
			batchSidecars = append(batchSidecars, mcpInstallSidecar{
				Name: tools.BM25SearchToolName, AgentID: target.ID,
				Kind: mcpInstallDiscoveryBM25,
			})
		}
		if hasDiscoverableServer && regexFactory != nil {
			live := tools.NewRegexSearchTool(
				target.Registry,
				discovery.TTL,
				discovery.MaxSearchResults,
			)
			expected, expectedErr := expectedDiscoveryRefresh(
				target.Registry,
				tools.RegexSearchToolName,
				mcpInstallDiscoveryRegex,
			)
			if expectedErr != nil {
				return stagedMCPGeneration{}, fmt.Errorf(
					"agent %q: %w",
					target.ID,
					expectedErr,
				)
			}
			batch.Installs = append(batch.Installs, tools.FactoryBackedInstall{
				Live: live, Factory: regexFactory, Expected: expected,
			})
			batchSidecars = append(batchSidecars, mcpInstallSidecar{
				Name: tools.RegexSearchToolName, AgentID: target.ID,
				Kind: mcpInstallDiscoveryRegex,
			})
		}

		if len(batch.Installs) == 0 {
			continue
		}
		batchIndex := len(stage.Batches)
		stage.Batches = append(stage.Batches, batch)
		for installIndex := range batchSidecars {
			batchSidecars[installIndex].BatchIndex = batchIndex
			batchSidecars[installIndex].InstallIndex = installIndex
			stage.Sidecars = append(stage.Sidecars, batchSidecars[installIndex])
		}
	}
	return stage, nil
}

func expectedMCPRefresh(
	registry *tools.ToolRegistry,
	name, serverName, remoteName string,
) (tools.Tool, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP tool registry is nil")
	}
	if !registry.AllowsRegistration(name) {
		return nil, nil
	}
	existing, occupied := registry.GetRegistered(name)
	if !occupied {
		return nil, nil
	}
	existingMCP, isMCP := existing.(*tools.MCPTool)
	if !isMCP || existingMCP == nil {
		return nil, fmt.Errorf(
			"%w %q for %q/%q conflicts with an existing tool",
			mcp.ErrCanonicalToolNameCollision,
			name,
			serverName,
			remoteName,
		)
	}
	existingServer, existingRemote := existingMCP.MCPIdentity()
	if existingServer == serverName && existingRemote == remoteName {
		return existing, nil
	}
	return nil, &mcp.CanonicalToolNameCollisionError{
		Name: name,
		First: mcp.ToolIdentity{
			Server: existingServer,
			Tool:   existingRemote,
		},
		Second: mcp.ToolIdentity{Server: serverName, Tool: remoteName},
	}
}

func expectedDiscoveryRefresh(
	registry *tools.ToolRegistry,
	name string,
	kind mcpInstallKind,
) (tools.Tool, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP discovery tool registry is nil")
	}
	if !registry.AllowsRegistration(name) {
		return nil, nil
	}
	existing, occupied := registry.GetRegistered(name)
	if !occupied {
		return nil, nil
	}
	switch kind {
	case mcpInstallDiscoveryBM25:
		candidate, typed := existing.(*tools.BM25SearchTool)
		if typed && candidate != nil {
			return existing, nil
		}
	case mcpInstallDiscoveryRegex:
		candidate, typed := existing.(*tools.RegexSearchTool)
		if typed && candidate != nil {
			return existing, nil
		}
	default:
		return nil, fmt.Errorf("unsupported MCP discovery install kind %d", kind)
	}
	return nil, fmt.Errorf(
		"MCP discovery tool %q conflicts with an existing %T",
		name,
		existing,
	)
}

func aggregateMCPAdmissions(
	stage stagedMCPGeneration,
	admissions []tools.FactoryBackedAdmission,
) (admittedMCPGeneration, error) {
	if len(admissions) != len(stage.Sidecars) {
		return admittedMCPGeneration{}, fmt.Errorf(
			"MCP admission count %d does not match staged count %d",
			len(admissions),
			len(stage.Sidecars),
		)
	}
	type serverAccumulator struct {
		names    []string
		deferred bool
		set      bool
	}
	type agentAccumulator struct {
		servers  map[string]serverAccumulator
		useBM25  bool
		useRegex bool
	}
	byAgent := make(map[string]*agentAccumulator)
	totalRegistrations := 0
	for index, admission := range admissions {
		sidecar := stage.Sidecars[index]
		if admission.BatchIndex != sidecar.BatchIndex ||
			admission.InstallIndex != sidecar.InstallIndex ||
			admission.Name != sidecar.Name {
			return admittedMCPGeneration{}, fmt.Errorf(
				"MCP admission %d does not match its staged sidecar",
				index,
			)
		}
		if !admission.Admitted {
			if admission.Replaced {
				return admittedMCPGeneration{}, fmt.Errorf(
					"denied MCP admission %d reports a replacement",
					index,
				)
			}
			continue
		}
		if sidecar.AgentID == "" || sidecar.AgentID != strings.TrimSpace(sidecar.AgentID) {
			return admittedMCPGeneration{}, fmt.Errorf(
				"admitted MCP sidecar %d has an invalid agent ID",
				index,
			)
		}
		accumulator := byAgent[sidecar.AgentID]
		if accumulator == nil {
			accumulator = &agentAccumulator{
				servers: make(map[string]serverAccumulator),
			}
			byAgent[sidecar.AgentID] = accumulator
		}
		switch sidecar.Kind {
		case mcpInstallRemote:
			if sidecar.ServerName == "" {
				return admittedMCPGeneration{}, fmt.Errorf(
					"admitted MCP tool %q has no server sidecar",
					sidecar.Name,
				)
			}
			server := accumulator.servers[sidecar.ServerName]
			if server.set && server.deferred != sidecar.Deferred {
				return admittedMCPGeneration{}, fmt.Errorf(
					"MCP server %q has inconsistent deferred admissions",
					sidecar.ServerName,
				)
			}
			server.names = append(server.names, sidecar.Name)
			server.deferred = sidecar.Deferred
			server.set = true
			accumulator.servers[sidecar.ServerName] = server
			totalRegistrations++
		case mcpInstallDiscoveryBM25:
			accumulator.useBM25 = true
		case mcpInstallDiscoveryRegex:
			accumulator.useRegex = true
		default:
			return admittedMCPGeneration{}, fmt.Errorf(
				"unsupported admitted MCP install kind %d",
				sidecar.Kind,
			)
		}
	}

	agentIDs := make([]string, 0, len(byAgent))
	for agentID := range byAgent {
		agentIDs = append(agentIDs, agentID)
	}
	sort.Strings(agentIDs)
	result := admittedMCPGeneration{
		Agents:             make([]admittedMCPAgent, 0, len(agentIDs)),
		TotalRegistrations: totalRegistrations,
	}
	for _, agentID := range agentIDs {
		accumulator := byAgent[agentID]
		serverNames := make([]string, 0, len(accumulator.servers))
		for serverName := range accumulator.servers {
			serverNames = append(serverNames, serverName)
		}
		sort.Strings(serverNames)
		agent := admittedMCPAgent{
			AgentID: agentID, UseBM25: accumulator.useBM25,
			UseRegex: accumulator.useRegex,
			Servers:  make([]admittedMCPServer, 0, len(serverNames)),
		}
		hasDeferredRemote := false
		for _, serverName := range serverNames {
			server := accumulator.servers[serverName]
			sort.Strings(server.names)
			for index := 1; index < len(server.names); index++ {
				if server.names[index-1] == server.names[index] {
					return admittedMCPGeneration{}, fmt.Errorf(
						"MCP server %q repeats admitted tool %q",
						serverName,
						server.names[index],
					)
				}
			}
			agent.Servers = append(agent.Servers, admittedMCPServer{
				Name: serverName, ToolCount: len(server.names),
				ToolNames: append([]string(nil), server.names...),
				Deferred:  server.deferred,
			})
			if server.deferred && len(server.names) > 0 {
				hasDeferredRemote = true
			}
		}
		if !hasDeferredRemote {
			agent.UseBM25 = false
			agent.UseRegex = false
		}
		if agent.UseBM25 {
			agent.DiscoveryToolNames = append(
				agent.DiscoveryToolNames,
				tools.BM25SearchToolName,
			)
		}
		if agent.UseRegex {
			agent.DiscoveryToolNames = append(
				agent.DiscoveryToolNames,
				tools.RegexSearchToolName,
			)
		}
		result.Agents = append(result.Agents, agent)
	}
	return result, nil
}
