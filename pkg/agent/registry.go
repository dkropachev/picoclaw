package agent

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentRegistry manages multiple agent instances and routes messages to them.
type AgentRegistry struct {
	cfg               *config.Config
	agents            map[string]*AgentInstance
	resolver          *routing.RouteResolver
	executionPolicy   isolation.ExecutionPolicy
	diagnosticPolicy  logger.DiagnosticPolicy
	bootstrapProvider providers.LLMProvider
	borrowedProviders []providers.LLMProvider
	mu                sync.RWMutex
}

type agentRegistryConstructionGuard struct {
	registry *AgentRegistry
}

func (guard *agentRegistryConstructionGuard) cleanupPanic() {
	recovered := recover()
	if recovered == nil {
		return
	}
	guard.registry.CloseCandidate()
	panic(recovered)
}

// NewAgentRegistry creates a registry from config, instantiating all agents.
func NewAgentRegistry(
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentRegistry {
	isolationCfg := config.DefaultConfig().Isolation
	if cfg != nil {
		isolationCfg = cfg.Isolation
	}
	return NewAgentRegistryWithRuntimePolicies(
		cfg,
		provider,
		isolation.NewExecutionPolicy(isolationCfg),
		logger.DiagnosticPolicy{},
	)
}

// NewAgentRegistryWithExecutionPolicy constructs every agent process owner
// from one exact immutable runtime-generation policy.
func NewAgentRegistryWithExecutionPolicy(
	cfg *config.Config,
	provider providers.LLMProvider,
	policy isolation.ExecutionPolicy,
) *AgentRegistry {
	return NewAgentRegistryWithRuntimePolicies(
		cfg,
		provider,
		policy,
		logger.DiagnosticPolicy{},
	)
}

// NewAgentRegistryWithRuntimePolicies constructs every agent process owner
// from one complete immutable runtime-generation policy tuple.
func NewAgentRegistryWithRuntimePolicies(
	cfg *config.Config,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
) *AgentRegistry {
	return newAgentRegistryWithRuntimePolicies(
		cfg,
		provider,
		executionPolicy,
		diagnosticPolicy,
		nil,
		mustAgentRuntimeFileMutationProtectedRoots("", cfg),
	)
}

func newAgentRegistryWithRuntimePolicies(
	cfg *config.Config,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
	providerGeneration *agentRegistryProviderGeneration,
	fileMutationProtectedRoots []string,
) *AgentRegistry {
	fileMutationProtectedRoots = cloneAgentRuntimeFileMutationProtectedRoots(fileMutationProtectedRoots)
	var protectedRootErr error
	fileMutationProtectedRoots, protectedRootErr = appendAgentWorkspaceSQLiteProtectedRoots(
		fileMutationProtectedRoots,
		cfg,
	)
	if protectedRootErr != nil {
		panic(fmt.Sprintf("build workspace file-mutation policy: %v", protectedRootErr))
	}
	gitRoots, gitRootErr := agentGitWorkspaceFileMutationProtectedRoots(cfg)
	if gitRootErr != nil {
		panic(fmt.Sprintf("build Git workspace file-mutation policy: %v", gitRootErr))
	}
	fileMutationProtectedRoots = append(fileMutationProtectedRoots, gitRoots...)
	agentConfigs := cfg.Agents.List
	mutationWorkspaces, workspaceErr := agentRegistryFileMutationWorkspaces(cfg, agentConfigs)
	if workspaceErr != nil {
		panic(fmt.Sprintf("build registry file-mutation workspaces: %v", workspaceErr))
	}
	for _, workspace := range mutationWorkspaces {
		fileMutationProtectedRoots, protectedRootErr = appendAgentCompleteWorkspaceFileMutationProtectedRoots(
			fileMutationProtectedRoots,
			workspace,
			cfg,
		)
		if protectedRootErr != nil {
			panic(fmt.Sprintf("build registry file-mutation policy: %v", protectedRootErr))
		}
	}
	localCIRoots := mustAgentLocalCIEvidenceFileMutationProtectedRoots(cfg)
	fileMutationProtectedRoots = append(
		fileMutationProtectedRoots,
		localCIRoots...,
	)
	identityExactRoots := cloneAgentRuntimeFileMutationProtectedRoots(
		fileMutationProtectedRoots,
	)
	identityGeneration, identityErr := newAgentFileMutationIdentityGeneration(
		mutationWorkspaces,
		cfg,
		identityExactRoots,
		fileMutationProtectedRoots,
	)
	if identityErr != nil {
		panic(fmt.Sprintf("build registry file-mutation identity catalog: %v", identityErr))
	}
	registry := &AgentRegistry{
		cfg:               cfg,
		agents:            make(map[string]*AgentInstance),
		resolver:          routing.NewRouteResolver(cfg),
		executionPolicy:   executionPolicy,
		diagnosticPolicy:  diagnosticPolicy,
		bootstrapProvider: provider,
	}
	if providerGeneration != nil {
		registry.borrowedProviders = providerGeneration.providerSet()
	}
	defer (&agentRegistryConstructionGuard{registry: registry}).cleanupPanic()
	if len(agentConfigs) == 0 {
		implicitAgent := &config.AgentConfig{
			ID:      "main",
			Default: true,
		}
		instance := newAgentInstanceWithRuntimePolicies(
			implicitAgent,
			&cfg.Agents.Defaults,
			cfg,
			provider,
			executionPolicy,
			diagnosticPolicy,
			providerGeneration.bindingsForAgent("main"),
			cloneAgentRuntimeFileMutationProtectedRoots(fileMutationProtectedRoots),
			identityGeneration,
		)
		if providerGeneration != nil {
			direct := providerGeneration.directForAgent("main")
			instance.Provider = direct.primary
			instance.LightProvider = direct.light
		}
		registry.agents["main"] = instance
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentCreatedImplicitMainAgentNoAgentsListConfigured,
			logger.NewSafeFields(),
		)
	} else {
		for i := range agentConfigs {
			ac := &agentConfigs[i]
			id := routing.NormalizeAgentID(ac.ID)
			instance := newAgentInstanceWithRuntimePolicies(
				ac,
				&cfg.Agents.Defaults,
				cfg,
				provider,
				executionPolicy,
				diagnosticPolicy,
				providerGeneration.bindingsForAgent(id),
				cloneAgentRuntimeFileMutationProtectedRoots(fileMutationProtectedRoots),
				identityGeneration,
			)
			if providerGeneration != nil {
				direct := providerGeneration.directForAgent(id)
				instance.Provider = direct.primary
				instance.LightProvider = direct.light
			}
			registry.agents[id] = instance
			logger.InfoSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentRegisteredAgent,
				logger.NewSafeFields(
					agentDiagnosticAgentField(id),
					agentDiagnosticWorkspaceField(instance.Workspace),
					agentDiagnosticModelField(instance.Model),
				),
			)
		}
	}

	for _, instance := range registry.agents {
		if instance.ContextBuilder != nil {
			instance.ContextBuilder.WithAgentDiscovery(instance.ID, registry.ListSpawnableAgents)
		}
	}

	return registry
}

func agentRegistryFileMutationWorkspaces(
	cfg *config.Config,
	agentConfigs []config.AgentConfig,
) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	candidates := make([]string, 0, len(agentConfigs)+2)
	if configured := cfg.WorkspacePath(); configured != "" {
		candidates = append(candidates, configured)
	}
	if len(agentConfigs) == 0 {
		if implicit := resolveAgentWorkspace(nil, &cfg.Agents.Defaults); implicit != "" {
			candidates = append(candidates, implicit)
		}
	} else {
		for index := range agentConfigs {
			workspace := resolveAgentWorkspace(&agentConfigs[index], &cfg.Agents.Defaults)
			if workspace != "" {
				candidates = append(candidates, workspace)
			}
		}
	}
	return normalizeAgentFileMutationWorkspaces(candidates)
}

// agentRegistryCumulativeFileMutationProtectedRoots captures the complete
// published generation. Reload uses it as the base for the next generation so
// retired workspaces and custom state roots never become writable by a later
// model generation.
func agentRegistryCumulativeFileMutationProtectedRoots(
	registry *AgentRegistry,
	previous []string,
) []string {
	roots := append([]string(nil), previous...)
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		seen[agentFileMutationWorkspaceKey(filepath.Clean(root))] = struct{}{}
	}
	if registry == nil {
		return roots
	}
	registry.mu.RLock()
	agents := make([]*AgentInstance, 0, len(registry.agents))
	for _, agent := range registry.agents {
		agents = append(agents, agent)
	}
	registry.mu.RUnlock()
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		for _, root := range agent.fileMutationProtectedRoots {
			key := agentFileMutationWorkspaceKey(filepath.Clean(root))
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			roots = append(roots, root)
		}
	}
	return roots
}

// CloseCandidate closes an unpublished registry and every internally-created
// stateful candidate provider, while retaining the externally supplied
// bootstrap provider owned by the reload caller.
func (r *AgentRegistry) CloseCandidate() {
	if r == nil {
		return
	}
	var providersToClose []providers.LLMProvider
	r.mu.RLock()
	agents := make([]*AgentInstance, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	bootstrap := r.bootstrapProvider
	borrowed := r.borrowedProviders
	r.mu.RUnlock()
	agentCandidateProvidersMu.RLock()
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		for _, candidateProvider := range agent.CandidateProviders {
			if candidateProvider != nil && !sameLLMProvider(candidateProvider, bootstrap) {
				retained := false
				for _, borrowedProvider := range borrowed {
					if sameLLMProvider(candidateProvider, borrowedProvider) {
						retained = true
						break
					}
				}
				if retained {
					continue
				}
				duplicate := false
				for _, queued := range providersToClose {
					if sameLLMProvider(queued, candidateProvider) {
						duplicate = true
						break
					}
				}
				if !duplicate {
					providersToClose = append(providersToClose, candidateProvider)
				}
			}
		}
	}
	agentCandidateProvidersMu.RUnlock()
	r.Close()
	for _, candidateProvider := range providersToClose {
		closeProviderIfStateful(candidateProvider)
	}
}

// GetAgent returns the agent instance for a given ID.
func (r *AgentRegistry) GetAgent(agentID string) (*AgentInstance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id := routing.NormalizeAgentID(agentID)
	agent, ok := r.agents[id]
	return agent, ok
}

// ResolveRoute determines which agent handles the normalized inbound context.
func (r *AgentRegistry) ResolveRoute(inbound bus.InboundContext) routing.ResolvedRoute {
	return r.resolver.ResolveRoute(inbound)
}

// ListAgentIDs returns all registered agent IDs.
func (r *AgentRegistry) ListAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

func (r *AgentRegistry) allowedMCPServers() map[string]struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.agents) == 0 {
		return nil
	}

	union := make(map[string]struct{})
	for _, agent := range r.agents {
		if agent == nil {
			continue
		}
		if agent.MCPServerAllowlist == nil {
			return nil
		}
		for serverName := range agent.MCPServerAllowlist {
			union[serverName] = struct{}{}
		}
	}

	return union
}

// CanSpawnSubagent checks if parentAgentID is allowed to spawn targetAgentID.
func (r *AgentRegistry) CanSpawnSubagent(parentAgentID, targetAgentID string) bool {
	parent, ok := r.GetAgent(parentAgentID)
	if !ok {
		return false
	}
	return agentAllowsSubagent(parent, routing.NormalizeAgentID(targetAgentID))
}

func agentAllowsSubagent(parent *AgentInstance, targetNorm string) bool {
	if parent == nil ||
		targetNorm == routing.NormalizeAgentID(parent.ID) ||
		parent.Subagents == nil ||
		parent.Subagents.AllowAgents == nil {
		return false
	}
	for _, allowed := range parent.Subagents.AllowAgents {
		if allowed == "*" {
			return true
		}
		if routing.NormalizeAgentID(allowed) == targetNorm {
			return true
		}
	}
	return false
}

func agentHasSpawnTool(agent *AgentInstance) bool {
	if agent == nil || agent.Tools == nil {
		return false
	}
	_, ok := agent.Tools.Get("spawn")
	return ok
}

// ForEachTool calls fn for every tool registered under the given name
// across all agents. This is useful for propagating dependencies (e.g.
// MediaStore) to tools after registry construction.
func (r *AgentRegistry) ForEachTool(name string, fn func(tools.Tool)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, agent := range r.agents {
		if t, ok := agent.Tools.Get(name); ok {
			fn(t)
		}
	}
}

// Close releases resources held by all registered agents.
func (r *AgentRegistry) Close() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, agent := range r.agents {
		if err := agent.Close(); err != nil {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToCloseAgent,
				logger.NewSafeFields(
					agentDiagnosticAgentField(agent.ID),
					agentDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
		}
	}
}

// GetDefaultAgent returns the default agent instance.
func (r *AgentRegistry) GetDefaultAgent() *AgentInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id := r.defaultAgentIDLocked(); id != "" {
		if agent, ok := r.agents[id]; ok {
			return agent
		}
	}
	for id := range r.agents {
		return r.agents[id]
	}
	return nil
}
