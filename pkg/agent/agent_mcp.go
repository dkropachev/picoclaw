// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"errors"
	"fmt"
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
	initMu    sync.Mutex
	initOnce  sync.Once
	mu        sync.RWMutex
	manager   *mcp.Manager
	initErr   error
	installer mcpFactoryBackedInstaller
}

func (r *mcpRuntime) do(initialize func()) error {
	r.initMu.Lock()
	defer r.initMu.Unlock()

	r.initOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				r.setInitErr(fmt.Errorf("initialize MCP generation: panic: %v", recovered))
			}
		}()
		if initialize == nil {
			r.setInitErr(fmt.Errorf("MCP initializer is nil"))
			return
		}
		initialize()
	})
	return r.getInitErr()
}

func (r *mcpRuntime) installFactoryBacked(
	batches []tools.FactoryBackedBatch,
) ([]tools.FactoryBackedAdmission, error) {
	install := r.installer
	if install == nil {
		install = tools.InstallFactoryBackedTransaction
	}
	return install(batches)
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
		if err := al.initializeMCPGeneration(ctx, cfg, registry, mcpCfg); err != nil {
			al.mcp.setInitErr(err)
			logger.WarnCF(
				"agent",
				"Failed to initialize MCP generation",
				map[string]any{"error": err.Error()},
			)
		}
	})
}

type mcpManagerOwnership uint8

const (
	mcpManagerPrivate mcpManagerOwnership = iota
	mcpManagerRegistryCommitted
	mcpManagerRuntimePublished
)

// initializeMCPGeneration owns one candidate manager from connection through
// the all-registry tool commit. Transaction success is the irreversible
// ownership boundary: published wrappers already borrow the candidate, so no
// later prompt or projection failure may close it beneath them.
func (al *AgentLoop) initializeMCPGeneration(
	ctx context.Context,
	cfg *config.Config,
	registry *AgentRegistry,
	mcpCfg config.MCPConfig,
) (returnErr error) {
	discovery, err := resolveMCPDiscoverySettings(mcpCfg.Discovery)
	if err != nil {
		return err
	}
	agents, err := snapshotMCPCatalogAgents(registry)
	if err != nil {
		return err
	}

	manager := mcp.NewManagerWithExecutionPolicy(
		registry.executionPolicy,
		mcp.WithRuntimeEvents(al.runtimeEvents),
	)
	ownership := mcpManagerPrivate
	defer func() {
		recovered := recover()
		if ownership == mcpManagerPrivate {
			if recovered != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("initialize MCP generation: panic: %v", recovered),
				)
			}
			if closeErr := manager.Close(); closeErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("close private MCP manager: %w", closeErr),
				)
			}
			return
		}

		if ownership == mcpManagerRegistryCommitted {
			al.mcp.setManager(manager)
			ownership = mcpManagerRuntimePublished
		}
		if recovered != nil {
			logger.ErrorCF(
				"agent",
				"MCP post-commit publication panicked; retained committed manager",
				map[string]any{"panic": fmt.Sprint(recovered)},
			)
			returnErr = nil
		}
	}()

	defaultAgent := registry.GetDefaultAgent()
	workspacePath := cfg.WorkspacePath()
	if defaultAgent != nil && defaultAgent.Workspace != "" {
		workspacePath = defaultAgent.Workspace
	}
	if loadErr := manager.LoadFromMCPConfig(ctx, mcpCfg, workspacePath); loadErr != nil {
		return fmt.Errorf("failed to load MCP servers: %w", loadErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("MCP initialization canceled after server load: %w", contextErr)
	}

	servers := manager.GetServers()
	if len(servers) == 0 {
		return fmt.Errorf("MCP initialization connected no enabled server")
	}
	stage, err := stageMCPGeneration(
		manager,
		servers,
		mcpCfg,
		discovery,
		agents,
		al.runtimeEvents,
	)
	if err != nil {
		return fmt.Errorf("stage MCP factory catalog: %w", err)
	}
	potentialPrompts, projectionErr := allAdmittedMCPProjection(stage)
	if projectionErr != nil {
		return fmt.Errorf("prevalidate MCP admission projection: %w", projectionErr)
	}
	if _, promptErr := prepareMCPAdmissionPrompts(registry, potentialPrompts); promptErr != nil {
		return fmt.Errorf("prevalidate MCP admission prompts: %w", promptErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("MCP initialization canceled before catalog commit: %w", contextErr)
	}

	admissions, err := al.mcp.installFactoryBacked(stage.Batches)
	if err != nil {
		return fmt.Errorf("install MCP factory catalog: %w", err)
	}
	ownership = mcpManagerRegistryCommitted
	// No fallible operation may run between transaction success and this
	// transfer. The deferred guard repeats the transfer if an unexpected panic
	// occurs in this tiny boundary.
	al.mcp.setManager(manager)
	ownership = mcpManagerRuntimePublished

	summary, err := aggregateMCPAdmissions(stage, admissions)
	if err != nil {
		logger.ErrorCF(
			"agent",
			"MCP admission projection failed after catalog commit",
			map[string]any{"error": err.Error()},
		)
		return nil
	}
	prompts, err := prepareMCPAdmissionPrompts(registry, summary)
	if err != nil {
		logger.ErrorCF(
			"agent",
			"MCP prompt preparation failed after catalog commit",
			map[string]any{"error": err.Error()},
		)
		return nil
	}
	if err := applyMCPAdmissionPrompts(prompts); err != nil {
		logger.WarnCF(
			"agent",
			"MCP prompt publication was incomplete",
			map[string]any{"error": err.Error()},
		)
	}

	logger.InfoCF("agent", "MCP factory catalog installed successfully", map[string]any{
		"server_count":        len(servers),
		"unique_tools":        stage.UniqueTools,
		"total_registrations": summary.TotalRegistrations,
		"agent_count":         stage.AgentCount,
	})
	return nil
}

type preparedMCPAgentPrompts struct {
	agentID   string
	builder   *ContextBuilder
	servers   []mcpServerPromptContributor
	discovery *toolDiscoveryPromptContributor
}

func allAdmittedMCPProjection(
	stage stagedMCPGeneration,
) (admittedMCPGeneration, error) {
	admissions := make([]tools.FactoryBackedAdmission, len(stage.Sidecars))
	for index, sidecar := range stage.Sidecars {
		admissions[index] = tools.FactoryBackedAdmission{
			BatchIndex: sidecar.BatchIndex, InstallIndex: sidecar.InstallIndex,
			Name: sidecar.Name, Admitted: true,
		}
	}
	return aggregateMCPAdmissions(stage, admissions)
}

func prepareMCPAdmissionPrompts(
	registry *AgentRegistry,
	summary admittedMCPGeneration,
) ([]preparedMCPAgentPrompts, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP prompt agent registry is nil")
	}
	prepared := make([]preparedMCPAgentPrompts, 0, len(summary.Agents))
	for _, admittedAgent := range summary.Agents {
		agent, ok := registry.GetAgent(admittedAgent.AgentID)
		if !ok || agent == nil || agent.ContextBuilder == nil {
			return nil, fmt.Errorf(
				"MCP prompt agent %q is unavailable",
				admittedAgent.AgentID,
			)
		}
		item := preparedMCPAgentPrompts{
			agentID: admittedAgent.AgentID,
			builder: agent.ContextBuilder,
			servers: make([]mcpServerPromptContributor, 0, len(admittedAgent.Servers)),
		}
		seenSources := make(map[PromptSourceID]string, len(admittedAgent.Servers))
		seenParts := make(map[string]string, len(admittedAgent.Servers))
		for _, server := range admittedAgent.Servers {
			contributor, err := newMCPServerPromptContributor(
				server.Name,
				server.ToolNames,
				admittedAgent.DiscoveryToolNames,
				server.Deferred,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"prepare MCP server prompt for agent %q: %w",
					admittedAgent.AgentID,
					err,
				)
			}
			sourceID := contributor.PromptSource().ID
			if previous, duplicate := seenSources[sourceID]; duplicate {
				return nil, fmt.Errorf(
					"MCP servers %q and %q collide on prompt source %q",
					previous,
					server.Name,
					sourceID,
				)
			}
			seenSources[sourceID] = server.Name
			partID := mcpPromptPartID(server.Name)
			if previous, duplicate := seenParts[partID]; duplicate {
				return nil, fmt.Errorf(
					"MCP servers %q and %q collide on prompt part %q",
					previous,
					server.Name,
					partID,
				)
			}
			seenParts[partID] = server.Name
			item.servers = append(item.servers, contributor)
		}
		if admittedAgent.UseBM25 || admittedAgent.UseRegex {
			item.discovery = &toolDiscoveryPromptContributor{
				useBM25:  admittedAgent.UseBM25,
				useRegex: admittedAgent.UseRegex,
			}
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func applyMCPAdmissionPrompts(prepared []preparedMCPAgentPrompts) error {
	var result error
	for _, agent := range prepared {
		batch := make([]PromptContributor, 0, len(agent.servers)+1)
		for _, contributor := range agent.servers {
			batch = append(batch, contributor)
		}
		if agent.discovery != nil {
			batch = append(batch, *agent.discovery)
		}
		if err := agent.builder.RegisterPromptContributors(batch); err != nil {
			result = errors.Join(result, fmt.Errorf(
				"register MCP prompt batch for agent %q: %w",
				agent.agentID,
				err,
			))
		}
	}
	return result
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
