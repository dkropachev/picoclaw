package gateway

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/reviews"
)

func configuredReviewAttentionPolicySource(
	cfg *config.Config,
) (*reviews.ConfigAttentionPolicySource, error) {
	if cfg == nil {
		return nil, fmt.Errorf("review attention configuration is required")
	}
	source, err := reviews.NewConfigAttentionPolicySource(
		cfg.Reviews.Attention.Global,
		cfg.Reviews.Attention.Repositories,
	)
	if err != nil {
		return nil, fmt.Errorf("validate review attention policies: %w", err)
	}
	return source, nil
}

func validateConfiguredReviewAttentionAgents(
	source *reviews.ConfigAttentionPolicySource,
	agentLoop *agent.AgentLoop,
) error {
	if source == nil {
		return fmt.Errorf("review attention policy source is required")
	}
	agentIDs := source.AgentIDs()
	if len(agentIDs) == 0 {
		return nil
	}
	if agentLoop == nil || agentLoop.GetRegistry() == nil {
		return fmt.Errorf("review attention AI gates require the agent runtime")
	}
	registry := agentLoop.GetRegistry()
	working := make(map[string]struct{}, len(source.WorkingContextAgentIDs()))
	for _, agentID := range source.WorkingContextAgentIDs() {
		working[agentID] = struct{}{}
	}
	for _, agentID := range agentIDs {
		runtimeAgent, ok := registry.GetAgent(agentID)
		if !ok || runtimeAgent == nil || runtimeAgent.ID != agentID {
			return fmt.Errorf("review attention agent %q is unavailable", agentID)
		}
		if _, requiresSession := working[agentID]; requiresSession &&
			runtimeAgent.Sessions == nil {
			return fmt.Errorf(
				"review attention working-context agent %q has no session store",
				agentID,
			)
		}
	}
	return nil
}
