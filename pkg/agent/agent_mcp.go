// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type mcpRuntime struct {
	// initMu serializes every operation that executes or replaces initOnce.
	// sync.Once is safe for concurrent Do calls, but assigning a fresh Once
	// while another Do is running is not. reset and takeManager therefore join
	// the same protocol as do.
	initMu   sync.Mutex
	initOnce sync.Once
	mu       sync.RWMutex
	manager  *mcp.Manager
	initErr  error
}

func (r *mcpRuntime) do(initialize func()) error {
	r.initMu.Lock()
	defer r.initMu.Unlock()

	r.initOnce.Do(initialize)
	return r.getInitErr()
}

func (r *mcpRuntime) reset() *mcp.Manager {
	r.initMu.Lock()
	defer r.initMu.Unlock()

	r.mu.Lock()
	manager := r.manager
	r.manager = nil
	r.initErr = nil
	r.initOnce = sync.Once{}
	r.mu.Unlock()
	return manager
}

func (r *mcpRuntime) setManager(manager *mcp.Manager) {
	r.mu.Lock()
	r.manager = manager
	r.initErr = nil
	r.mu.Unlock()
}

func (r *mcpRuntime) setInitErr(err error) {
	r.mu.Lock()
	r.initErr = err
	r.mu.Unlock()
}

func (r *mcpRuntime) getInitErr() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.initErr
}

func (r *mcpRuntime) takeManager() *mcp.Manager {
	r.initMu.Lock()
	defer r.initMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	manager := r.manager
	r.manager = nil
	return manager
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.manager != nil
}

func (r *mcpRuntime) getManager() *mcp.Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.manager
}

// EnsureMCPInitialized exposes the shared lazy MCP initialization path to
// runtimes that execute workflow tool steps without starting AgentLoop.Run.
func (al *AgentLoop) EnsureMCPInitialized(ctx context.Context) error {
	if al == nil {
		return fmt.Errorf("agent loop not configured")
	}
	return al.ensureMCPInitialized(ctx)
}

// ensureMCPInitialized snapshots one exact runtime generation and leases it
// before entering any manager/network initialization. Static no-op generations
// need no admission. A context that already owns this loop's lease is reused so
// callers inside a turn do not self-deadlock.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	if al == nil {
		return fmt.Errorf("agent loop not configured")
	}

	// Preserve the static no-op path without depending on runtime admission.
	// This matters during startup and shutdown: there is no manager or registry
	// mutation to protect when this exact captured generation cannot initialize
	// any MCP server. A reload that publishes an enabled generation initializes
	// that candidate itself while holding the reload pause.
	al.mu.RLock()
	cfg := al.cfg
	registry := al.registry
	al.mu.RUnlock()
	if !mcpGenerationNeedsRuntimeLease(cfg, registry) {
		return al.ensureMCPInitializedForGeneration(ctx, cfg, registry)
	}

	leaseCtx, releaseRuntime, err := al.acquireRuntimeUse(ctx)
	if err != nil {
		return err
	}
	defer releaseRuntime()

	al.mu.RLock()
	cfg = al.cfg
	registry = al.registry
	al.mu.RUnlock()

	return al.ensureMCPInitializedForGeneration(leaseCtx, cfg, registry)
}

func mcpGenerationNeedsRuntimeLease(
	cfg *config.Config,
	registry *AgentRegistry,
) bool {
	if cfg == nil || registry == nil || !cfg.Tools.IsToolEnabled("mcp") ||
		len(cfg.Tools.MCP.Servers) == 0 {
		return false
	}
	filtered := filterMCPConfigServers(
		cfg.Tools.MCP,
		registry.allowedMCPServers(),
	)
	for _, serverCfg := range filtered.Servers {
		if serverCfg.Enabled {
			return true
		}
	}
	return false
}

// ensureMCPInitializedForGeneration initializes MCP for the exact config and
// registry supplied by a caller that either owns a runtime-generation lease or
// holds the reload pause. It intentionally does not acquire admission itself:
// reload uses it while new runtime admissions are paused.
func (al *AgentLoop) ensureMCPInitializedForGeneration(
	ctx context.Context,
	cfg *config.Config,
	registry *AgentRegistry,
) error {
	if al == nil {
		return fmt.Errorf("agent loop not configured")
	}
	if cfg == nil {
		return fmt.Errorf("MCP config generation is not configured")
	}
	if !cfg.Tools.IsToolEnabled("mcp") {
		return nil
	}
	if registry == nil {
		return fmt.Errorf("MCP agent registry generation is not configured")
	}

	if cfg.Tools.MCP.Servers == nil || len(cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return nil
	}

	mcpCfg := filterMCPConfigServers(cfg.Tools.MCP, registry.allowedMCPServers())
	if mcpCfg.Servers == nil || len(mcpCfg.Servers) == 0 {
		logger.InfoCF(
			"agent",
			"No MCP servers selected after applying per-agent mcpServers allowlists",
			nil,
		)
		return nil
	}

	findValidServer := false
	for _, serverCfg := range mcpCfg.Servers {
		if serverCfg.Enabled {
			findValidServer = true
		}
	}
	if !findValidServer {
		logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
		return nil
	}

	return al.mcp.do(func() {
		mcpManager := mcp.NewManager(mcp.WithRuntimeEvents(al.runtimeEvents))

		defaultAgent := registry.GetDefaultAgent()
		workspacePath := cfg.WorkspacePath()
		if defaultAgent != nil && defaultAgent.Workspace != "" {
			workspacePath = defaultAgent.Workspace
		}

		if err := mcpManager.LoadFromMCPConfig(ctx, mcpCfg, workspacePath); err != nil {
			al.mcp.setInitErr(fmt.Errorf("failed to load MCP servers: %w", err))
			logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
				map[string]any{
					"error": err.Error(),
				})
			if closeErr := mcpManager.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager",
					map[string]any{
						"error": closeErr.Error(),
					})
			}
			return
		}

		// Register MCP tools for all agents
		servers := mcpManager.GetServers()
		if collisionErr := validateMCPToolRegistrations(
			servers,
			mcpCfg,
			registry,
		); collisionErr != nil {
			al.mcp.setInitErr(fmt.Errorf("ambiguous MCP tool registration: %w", collisionErr))
			logger.ErrorCF("agent", "Refusing ambiguous MCP tool registration",
				map[string]any{
					"error": collisionErr.Error(),
				})
			if closeErr := mcpManager.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager",
					map[string]any{
						"error": closeErr.Error(),
					})
			}
			return
		}
		uniqueTools := 0
		totalRegistrations := 0
		agentIDs := registry.ListAgentIDs()
		agentCount := len(agentIDs)

		for serverName, conn := range servers {
			uniqueTools += len(conn.Tools)

			// Determine whether this server's tools should be deferred (hidden).
			// Per-server "deferred" field takes precedence over the global Discovery.Enabled.
			serverCfg := mcpCfg.Servers[serverName]
			registerAsHidden := serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg)
			registeredToolsByAgent := make(map[string]map[string]struct{}, len(agentIDs))

			for _, tool := range conn.Tools {
				for _, agentID := range agentIDs {
					agent, ok := registry.GetAgent(agentID)
					if !ok {
						continue
					}
					if !agent.AllowsMCPServer(serverName) {
						logger.DebugCF("agent", "Skipped MCP tool registration by agent mcpServers allowlist",
							map[string]any{
								"agent_id": agentID,
								"server":   serverName,
								"tool":     tool.Name,
							})
						continue
					}

					mcpTool := tools.NewMCPTool(mcpManager, serverName, tool)
					toolName := mcpTool.Name()
					mcpTool.SetWorkspace(agent.Workspace)
					mcpTool.SetMaxInlineTextRunes(cfg.Tools.MCP.GetMaxInlineTextChars())
					mcpTool.SetEventPublisher(al.runtimeEvents)

					if registerAsHidden {
						agent.Tools.RegisterHidden(mcpTool)
					} else {
						agent.Tools.Register(mcpTool)
					}
					if !toolRegistryIncludes(agent.Tools, toolName) {
						continue
					}

					recordRegisteredMCPTool(registeredToolsByAgent, agentID, toolName)
					totalRegistrations++
					logger.DebugCF("agent", "Registered MCP tool",
						map[string]any{
							"agent_id": agentID,
							"server":   serverName,
							"tool":     tool.Name,
							"name":     toolName,
							"deferred": registerAsHidden,
						})
				}
			}

			for _, agentID := range agentIDs {
				agent, ok := registry.GetAgent(agentID)
				if !ok {
					continue
				}
				registerMCPServerPromptContributor(
					agentID,
					agent,
					serverName,
					len(registeredToolsByAgent[agentID]),
					registerAsHidden,
				)
			}
		}
		logger.InfoCF("agent", "MCP tools registered successfully",
			map[string]any{
				"server_count":        len(servers),
				"unique_tools":        uniqueTools,
				"total_registrations": totalRegistrations,
				"agent_count":         agentCount,
			})

		// Initializes Discovery Tools only if enabled by configuration
		if cfg.Tools.MCP.Enabled && cfg.Tools.MCP.Discovery.Enabled {
			useBM25 := cfg.Tools.MCP.Discovery.UseBM25
			useRegex := cfg.Tools.MCP.Discovery.UseRegex

			// Fail fast: If discovery is enabled but no search method is turned on
			if !useBM25 && !useRegex {
				al.mcp.setInitErr(fmt.Errorf(
					"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
				))
				if closeErr := mcpManager.Close(); closeErr != nil {
					logger.ErrorCF("agent", "Failed to close MCP manager",
						map[string]any{
							"error": closeErr.Error(),
						})
				}
				return
			}

			ttl := cfg.Tools.MCP.Discovery.TTL
			if ttl <= 0 {
				ttl = 5 // Default value
			}

			maxSearchResults := cfg.Tools.MCP.Discovery.MaxSearchResults
			if maxSearchResults <= 0 {
				maxSearchResults = 5 // Default value
			}

			logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
				"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
			})

			for _, agentID := range agentIDs {
				agent, ok := registry.GetAgent(agentID)
				if !ok {
					continue
				}
				if !agentHasDiscoverableMCPServers(cfg, agent.MCPServerAllowlist) {
					continue
				}

				if useRegex {
					agent.Tools.Register(tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
				}
				if useBM25 {
					agent.Tools.Register(tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
				}
			}
		}

		al.mcp.setManager(mcpManager)
	})
}

func validateCanonicalMCPToolNames(servers map[string]*mcp.ServerConnection) error {
	identities := make([]mcp.ToolIdentity, 0)
	for serverName, connection := range servers {
		if connection == nil {
			continue
		}
		for _, tool := range connection.Tools {
			if tool == nil {
				continue
			}
			identities = append(identities, mcp.ToolIdentity{
				Server: serverName,
				Tool:   tool.Name,
			})
		}
	}
	return mcp.DetectCanonicalToolNameCollision(identities)
}

// validateMCPToolRegistrations preflights every MCP wrapper that would be
// admitted to an agent registry. It must run before the first registration so
// one conflicting built-in, local, or plugin tool cannot leave a partially
// exposed MCP surface.
func validateMCPToolRegistrations(
	servers map[string]*mcp.ServerConnection,
	mcpCfg config.MCPConfig,
	registry *AgentRegistry,
) error {
	if err := validateCanonicalMCPToolNames(servers); err != nil {
		return err
	}
	if registry == nil {
		return nil
	}

	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	serverNames := make([]string, 0, len(servers))
	for serverName := range servers {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)

	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}
		for _, serverName := range serverNames {
			connection := servers[serverName]
			if connection == nil || !agent.AllowsMCPServer(serverName) {
				continue
			}
			if serverCfg, configured := mcpCfg.Servers[serverName]; configured &&
				!serverCfg.Enabled {
				continue
			}
			for _, tool := range connection.Tools {
				if tool == nil {
					continue
				}
				name := mcp.CanonicalToolName(serverName, tool.Name)
				if !agent.Tools.AllowsRegistration(name) {
					continue
				}
				existing, occupied := agent.Tools.GetRegistered(name)
				if !occupied {
					continue
				}
				existingMCP, isMCPWrapper := existing.(*tools.MCPTool)
				if !isMCPWrapper {
					return fmt.Errorf(
						"%w %q for %q/%q conflicts with an existing tool in agent %q",
						mcp.ErrCanonicalToolNameCollision,
						name,
						serverName,
						tool.Name,
						agentID,
					)
				}
				existingServer, existingTool := existingMCP.MCPIdentity()
				if existingServer == serverName && existingTool == tool.Name {
					continue
				}
				return fmt.Errorf(
					"agent %q: %w",
					agentID,
					&mcp.CanonicalToolNameCollisionError{
						Name: name,
						First: mcp.ToolIdentity{
							Server: existingServer,
							Tool:   existingTool,
						},
						Second: mcp.ToolIdentity{
							Server: serverName,
							Tool:   tool.Name,
						},
					},
				)
			}
		}
	}
	return nil
}

func registerMCPServerPromptContributor(
	agentID string,
	agent *AgentInstance,
	serverName string,
	toolCount int,
	registerAsHidden bool,
) {
	if agent == nil || agent.ContextBuilder == nil || toolCount <= 0 {
		return
	}
	if err := agent.ContextBuilder.RegisterPromptContributor(mcpServerPromptContributor{
		serverName: serverName,
		toolCount:  toolCount,
		deferred:   registerAsHidden,
	}); err != nil {
		logger.WarnCF("agent", "Failed to register MCP prompt contributor",
			map[string]any{
				"agent_id": agentID,
				"server":   serverName,
				"error":    err.Error(),
			})
	}
}

func recordRegisteredMCPTool(
	registeredToolsByAgent map[string]map[string]struct{},
	agentID, toolName string,
) {
	if registeredToolsByAgent[agentID] == nil {
		registeredToolsByAgent[agentID] = make(map[string]struct{})
	}
	registeredToolsByAgent[agentID][toolName] = struct{}{}
}

func toolRegistryIncludes(registry *tools.ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	return registry.HasRegistered(name)
}

func filterMCPConfigServers(
	mcpCfg config.MCPConfig,
	allowed map[string]struct{},
) config.MCPConfig {
	if allowed == nil {
		return mcpCfg
	}

	filtered := mcpCfg
	filtered.Servers = make(map[string]config.MCPServerConfig)
	normalizedAllowed := make(map[string]struct{}, len(allowed))
	for serverName := range allowed {
		name := normalizeMCPServerName(serverName)
		if name == "" {
			continue
		}
		normalizedAllowed[name] = struct{}{}
	}
	for serverName, serverCfg := range mcpCfg.Servers {
		if _, ok := normalizedAllowed[normalizeMCPServerName(serverName)]; ok {
			filtered.Servers[serverName] = serverCfg
		}
	}

	return filtered
}

func agentHasDiscoverableMCPServers(cfg *config.Config, allowed map[string]struct{}) bool {
	if cfg == nil || !cfg.Tools.MCP.Enabled || !cfg.Tools.MCP.Discovery.Enabled {
		return false
	}

	filtered := filterMCPConfigServers(cfg.Tools.MCP, allowed)
	for _, serverCfg := range filtered.Servers {
		if serverCfg.Enabled && serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg) {
			return true
		}
	}

	return false
}

// serverIsDeferred reports whether an MCP server's tools should be registered
// as hidden (deferred/discovery mode).
//
// The per-server Deferred field takes precedence over the global discoveryEnabled
// default. When Deferred is nil, discoveryEnabled is used as the fallback.
func serverIsDeferred(discoveryEnabled bool, serverCfg config.MCPServerConfig) bool {
	if !discoveryEnabled {
		return false
	}
	if serverCfg.Deferred != nil {
		return *serverCfg.Deferred
	}
	return true
}
