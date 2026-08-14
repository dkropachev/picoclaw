package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

func newPRWorkspaceGateWorkingContextAcquire(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) prworkspace.GateWorkingContextRuntimeAcquire {
	return func(
		ctx context.Context,
		agentID string,
	) (context.Context, session.SessionStore, func(), error) {
		noop := func() {}
		if cfg == nil || agentLoop == nil {
			return ctx, nil, noop, errors.New("PR gate working-context runtime is not configured")
		}
		if agentID != strings.TrimSpace(agentID) || !routing.IsCanonicalAgentID(agentID) {
			return ctx, nil, noop, errors.New("PR gate working-context agent ID is not exact and canonical")
		}
		leaseCtx, release, err := agentLoop.AcquireRuntimeGeneration(ctx, cfg)
		if err != nil {
			return ctx, nil, noop, err
		}
		registry := agentLoop.GetRegistry()
		if registry == nil {
			release()
			return ctx, nil, noop, errors.New("PR gate working-context registry is unavailable")
		}
		runtimeAgent, ok := registry.GetAgent(agentID)
		if !ok || runtimeAgent == nil || runtimeAgent.ID != agentID || runtimeAgent.Sessions == nil {
			release()
			return ctx, nil, noop, fmt.Errorf("PR gate working-context agent %q is unavailable", agentID)
		}
		return leaseCtx, runtimeAgent.Sessions, release, nil
	}
}
