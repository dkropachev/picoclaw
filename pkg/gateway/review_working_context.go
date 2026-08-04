package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

func defaultReviewRuntimeAgent(agentLoop *agent.AgentLoop) (*agent.AgentInstance, error) {
	if agentLoop == nil {
		return nil, fmt.Errorf("review agent runtime is not configured")
	}
	registry := agentLoop.GetRegistry()
	if registry == nil {
		return nil, fmt.Errorf("review agent registry is not configured")
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, fmt.Errorf("review agent runtime has no default agent")
	}
	agentID := strings.TrimSpace(defaultAgent.ID)
	if !routing.IsCanonicalAgentID(agentID) {
		return nil, fmt.Errorf("review default agent ID is not canonical")
	}
	if agentID != defaultAgent.ID {
		return nil, fmt.Errorf("review default agent ID is not exact")
	}
	return defaultAgent, nil
}

func newReviewWorkingContextRuntimeAcquire(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) reviews.WorkingContextRuntimeAcquire {
	return func(
		ctx context.Context,
		agentID string,
	) (context.Context, session.SessionStore, func(), error) {
		if cfg == nil || agentLoop == nil {
			return ctx, nil, func() {}, fmt.Errorf("review runtime is not configured")
		}
		if !routing.IsCanonicalAgentID(agentID) || strings.TrimSpace(agentID) != agentID {
			return ctx, nil, func() {}, fmt.Errorf("review agent ID is not exact and canonical")
		}
		leaseCtx, release, err := agentLoop.AcquireRuntimeGeneration(ctx, cfg)
		if err != nil {
			return ctx, nil, func() {}, err
		}
		registry := agentLoop.GetRegistry()
		if registry == nil {
			release()
			return ctx, nil, func() {}, fmt.Errorf("review agent registry is not configured")
		}
		runtimeAgent, ok := registry.GetAgent(agentID)
		if !ok || runtimeAgent == nil || runtimeAgent.ID != agentID {
			release()
			return ctx, nil, func() {}, fmt.Errorf("review agent %q is unavailable", agentID)
		}
		if runtimeAgent.Sessions == nil {
			release()
			return ctx, nil, func() {}, fmt.Errorf(
				"review agent %q session store is unavailable",
				agentID,
			)
		}
		return leaseCtx, runtimeAgent.Sessions, release, nil
	}
}
