package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// NewControllerLocalRepairRunner resolves one controller-owned repair runner
// from the current agent generation. The caller must hold an
// AcquireRuntimeGeneration lease while constructing and using the runner; this
// method does not acquire or retain that lease itself.
//
// routingText is untrusted routing input. It is used only for the initial model
// selection, without conversation history or session affinity. The runner is
// bound to the first resolved candidate and never executes a fallback chain.
func (al *AgentLoop) NewControllerLocalRepairRunner(
	agentID string,
	routingText string,
) (*LocalRepairRunner, error) {
	if al == nil {
		return nil, errors.New("controller local repair agent loop is unavailable")
	}
	if agentID != strings.TrimSpace(agentID) || !routing.IsCanonicalAgentID(agentID) {
		return nil, errors.New("controller local repair agent ID must be exact and canonical")
	}

	al.mu.RLock()
	cfg := al.cfg
	registry := al.registry
	workspaces := al.gitWorkspaces
	al.mu.RUnlock()
	return al.newControllerLocalRepairRunner(
		cfg,
		registry,
		workspaces,
		0,
		false,
		agentID,
		routingText,
	)
}

// NewControllerLocalRepairRunnerWithRuntimeLease resolves a repair runner from
// the exact immutable generation carried by ctx. Run must receive the same
// still-live generation lease.
func (al *AgentLoop) NewControllerLocalRepairRunnerWithRuntimeLease(
	ctx context.Context,
	agentID string,
	routingText string,
) (*LocalRepairRunner, error) {
	if al == nil {
		return nil, errors.New("controller local repair agent loop is unavailable")
	}
	generation, err := al.runtimeGenerationFromLease(ctx)
	if err != nil {
		return nil, errors.New("controller local repair runtime lease is unavailable")
	}
	al.mu.RLock()
	workspaces := al.gitWorkspaces
	al.mu.RUnlock()
	return al.newControllerLocalRepairRunner(
		generation.cfg,
		generation.registry,
		workspaces,
		generation.id,
		true,
		agentID,
		routingText,
	)
}

func (al *AgentLoop) newControllerLocalRepairRunner(
	cfg *config.Config,
	registry *AgentRegistry,
	workspaces PinnedWorkspaceAcquirer,
	generationID uint64,
	strictRuntime bool,
	agentID string,
	routingText string,
) (*LocalRepairRunner, error) {
	if agentID != strings.TrimSpace(agentID) || !routing.IsCanonicalAgentID(agentID) {
		return nil, errors.New("controller local repair agent ID must be exact and canonical")
	}
	if cfg == nil {
		return nil, errors.New("controller local repair configuration is unavailable")
	}
	if registry == nil {
		return nil, errors.New("controller local repair agent registry is unavailable")
	}
	if localRepairNil(workspaces) {
		return nil, errors.New("controller local repair workspace manager is unavailable")
	}

	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil || agent.ID != agentID {
		return nil, errors.New("controller local repair agent is unavailable")
	}
	if agent.ConfigurationError != nil {
		return nil, errors.New("controller local repair agent configuration is invalid")
	}

	candidates, model, usedLight, activeRouter, routerSelection := al.selectCandidates(
		agent,
		routingText,
		nil,
		"",
		accountrouter.SelectReasonInitial,
	)
	if len(candidates) == 0 ||
		usedLight && agent.Router == nil ||
		activeRouter != nil && len(routerSelection.Candidates) == 0 {
		return nil, errors.New("controller local repair concrete model is unavailable")
	}
	selected := candidates[0]
	model = strings.TrimSpace(model)
	if model == "" || model != selected.Model {
		return nil, errors.New("controller local repair concrete model is unavailable")
	}

	provider := agent.candidateProviderForCandidate(selected)
	if localRepairNil(provider) && controllerRepairExactPrimary(agent, selected) {
		provider = agent.Provider
	}
	if localRepairNil(provider) {
		return nil, errors.New("controller local repair concrete provider is unavailable")
	}
	reasoningEffort, err := controllerLocalRepairReasoningEffort(cfg, agent, selected)
	if err != nil {
		return nil, errors.New("controller local repair agent configuration is invalid")
	}

	protectedRoots := append(
		[]string(nil),
		al.fileMutationProtectedRoots...,
	)
	if strings.TrimSpace(agent.Workspace) != "" {
		protectedRoots = append(
			protectedRoots,
			mustAgentWorkspaceFileMutationProtectedRoots(agent.Workspace)...,
		)
		protectedRoots = append(
			protectedRoots,
			mustAgentWorkspaceAccountRouterProtectedRoots(agent.Workspace)...,
		)
		protectedRoots = append(
			protectedRoots,
			agentSessionFileMutationProtectedRoots(agent.Workspace)...,
		)
	}
	runner, err := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:      workspaces,
		Provider:        provider,
		Model:           model,
		MaxIterations:   agent.MaxIterations,
		MaxTokens:       agent.MaxTokens,
		Temperature:     agent.Temperature,
		ReasoningEffort: reasoningEffort,
		ProtectedRoots: cloneAgentRuntimeFileMutationProtectedRoots(
			protectedRoots,
		),
	})
	if err != nil {
		return nil, errors.New("controller local repair agent configuration is invalid")
	}
	runner.runtimeLoop = al
	runner.generationID = generationID
	runner.strictRuntime = strictRuntime
	return runner, nil
}

// ControllerLocalRepairReady reports whether one exact current agent can be
// bound to at least one concrete controller-only repair target. The caller
// must hold the runtime-generation lease, the paused generation-construction
// boundary, or a generation-owned admission that drains before reload pause.
// Readiness never invokes a provider or creates session affinity.
func (al *AgentLoop) ControllerLocalRepairReady(agentID string) bool {
	if al == nil || agentID != strings.TrimSpace(agentID) ||
		!routing.IsCanonicalAgentID(agentID) {
		return false
	}

	al.mu.RLock()
	cfg := al.cfg
	registry := al.registry
	workspaces := al.gitWorkspaces
	al.mu.RUnlock()
	return controllerLocalRepairReadyForGeneration(
		cfg,
		registry,
		workspaces,
		agentID,
	)
}

// ControllerLocalRepairReadyWithRuntimeLease reports readiness only for the
// exact live generation carried by ctx.
func (al *AgentLoop) ControllerLocalRepairReadyWithRuntimeLease(
	ctx context.Context,
	agentID string,
) bool {
	if al == nil {
		return false
	}
	generation, err := al.runtimeGenerationFromLease(ctx)
	if err != nil {
		return false
	}
	al.mu.RLock()
	workspaces := al.gitWorkspaces
	al.mu.RUnlock()
	return controllerLocalRepairReadyForGeneration(
		generation.cfg,
		generation.registry,
		workspaces,
		agentID,
	)
}

func controllerLocalRepairReadyForGeneration(
	cfg *config.Config,
	registry *AgentRegistry,
	workspaces PinnedWorkspaceAcquirer,
	agentID string,
) bool {
	if agentID != strings.TrimSpace(agentID) ||
		!routing.IsCanonicalAgentID(agentID) {
		return false
	}
	if cfg == nil || registry == nil || localRepairNil(workspaces) {
		return false
	}
	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil || agent.ID != agentID || agent.ConfigurationError != nil {
		return false
	}

	if agent.ModelRouter == nil {
		return controllerRepairCandidateReady(
			cfg,
			workspaces,
			agent,
			firstRepairCandidate(agent.Candidates),
		)
	}
	seen := make(map[string]bool)
	for _, block := range agent.ModelRouter.Config.Blocks {
		if strings.TrimSpace(block.Type) != config.ModelRouterBlockTypeModel {
			continue
		}
		alias := strings.TrimSpace(block.Model)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		if router := buildAccountRouterWithAliases(
			cfg,
			agent.AccountRef,
			alias,
			agent.Fallbacks,
			agent.Workspace,
			agent.CandidateProviders,
			agent.executionPolicy,
		); router != nil {
			for _, account := range router.Accounts {
				if controllerRepairCandidateReady(
					cfg,
					workspaces,
					agent,
					firstRepairCandidate(account.Candidates),
				) {
					return true
				}
			}
			continue
		}
		candidates, err := candidatesForAccountAliases(
			cfg,
			agent.AccountRef,
			alias,
			agent.Fallbacks,
			agent.Workspace,
			agent.CandidateProviders,
			agent.executionPolicy,
		)
		if err == nil && controllerRepairCandidateReady(
			cfg,
			workspaces,
			agent,
			firstRepairCandidate(candidates),
		) {
			return true
		}
	}
	return false
}

func firstRepairCandidate(
	candidates []providers.FallbackCandidate,
) *providers.FallbackCandidate {
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0]
}

func controllerRepairCandidateReady(
	cfg *config.Config,
	workspaces PinnedWorkspaceAcquirer,
	agent *AgentInstance,
	candidate *providers.FallbackCandidate,
) bool {
	if agent == nil || candidate == nil || localRepairNil(workspaces) {
		return false
	}
	model := strings.TrimSpace(candidate.Model)
	if model == "" || model != candidate.Model {
		return false
	}
	provider := agent.candidateProviderForCandidate(*candidate)
	if localRepairNil(provider) && controllerRepairExactPrimary(agent, *candidate) {
		provider = agent.Provider
	}
	if localRepairNil(provider) {
		return false
	}
	reasoningEffort, err := controllerLocalRepairReasoningEffort(cfg, agent, *candidate)
	if err != nil {
		return false
	}
	_, err = NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:      workspaces,
		Provider:        provider,
		Model:           model,
		MaxIterations:   agent.MaxIterations,
		MaxTokens:       agent.MaxTokens,
		Temperature:     agent.Temperature,
		ReasoningEffort: reasoningEffort,
	})
	return err == nil
}

func controllerLocalRepairReasoningEffort(
	cfg *config.Config,
	agent *AgentInstance,
	candidate providers.FallbackCandidate,
) (string, error) {
	if cfg == nil || agent == nil {
		return "", nil
	}
	modelConfig := resolveActiveModelConfig(
		cfg,
		agent.Workspace,
		[]providers.FallbackCandidate{candidate},
		candidate.Model,
		candidate.Provider,
	)
	if modelConfig == nil {
		return "", nil
	}
	return normalizeLocalRepairReasoningEffort(modelConfig.ReasoningEffort)
}

func controllerRepairExactPrimary(
	agent *AgentInstance,
	selected providers.FallbackCandidate,
) bool {
	if agent == nil || len(agent.Candidates) == 0 || localRepairNil(agent.Provider) {
		return false
	}
	primary := agent.Candidates[0]
	return selected.StableKey() == primary.StableKey() &&
		providers.NormalizeProvider(selected.Provider) == providers.NormalizeProvider(primary.Provider) &&
		strings.TrimSpace(selected.Model) == strings.TrimSpace(primary.Model)
}
