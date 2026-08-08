package agent

import (
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

	runner, err := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:    workspaces,
		Provider:      provider,
		Model:         model,
		MaxIterations: agent.MaxIterations,
		MaxTokens:     agent.MaxTokens,
		Temperature:   agent.Temperature,
	})
	if err != nil {
		return nil, errors.New("controller local repair agent configuration is invalid")
	}
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
	if cfg == nil || registry == nil || localRepairNil(workspaces) {
		return false
	}
	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil || agent.ID != agentID || agent.ConfigurationError != nil {
		return false
	}

	if agent.ModelRouter == nil {
		return controllerRepairCandidateReady(workspaces, agent, firstRepairCandidate(agent.Candidates))
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
		); router != nil {
			for _, account := range router.Accounts {
				if controllerRepairCandidateReady(
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
		)
		if err == nil && controllerRepairCandidateReady(
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
	_, err := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:    workspaces,
		Provider:      provider,
		Model:         model,
		MaxIterations: agent.MaxIterations,
		MaxTokens:     agent.MaxTokens,
		Temperature:   agent.Temperature,
	})
	return err == nil
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
