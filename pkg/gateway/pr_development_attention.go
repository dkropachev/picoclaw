package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

// newPRDevelopmentAttentionWorkspaceFactory exposes the controller manager only
// through the bounded parked-tip review interface. The launcher calls this
// factory while its owner-runtime lease is held; this function deliberately
// does not acquire a nested generation lease.
func newPRDevelopmentAttentionWorkspaceFactory(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) prdevelopment.AttentionReviewWorkspaceFactory {
	if cfg == nil || agentLoop == nil {
		return nil
	}
	return newPRDevelopmentAttentionWorkspaceFactoryWithResolver(
		func() (prdevelopment.AttentionReviewWorkspace, error) {
			return agentLoop.ControllerGitWorkspaceManager()
		},
	)
}

func newPRDevelopmentAttentionWorkspaceFactoryWithResolver(
	resolve func() (prdevelopment.AttentionReviewWorkspace, error),
) prdevelopment.AttentionReviewWorkspaceFactory {
	if resolve == nil {
		return nil
	}
	return func() (prdevelopment.AttentionReviewWorkspace, error) {
		reader, err := resolve()
		if err != nil {
			return nil, err
		}
		if reader == nil {
			return nil, fmt.Errorf(
				"pull request development attention Git reader is unavailable",
			)
		}
		return &prDevelopmentAttentionGitReader{reader: reader}, nil
	}
}

type prDevelopmentAttentionGitReader struct {
	reader prdevelopment.AttentionReviewWorkspace
}

func (reader *prDevelopmentAttentionGitReader) SnapshotPinnedLineReview(
	ctx context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	if reader == nil || reader.reader == nil {
		return gitworkspace.PinnedLineReviewSnapshot{}, fmt.Errorf(
			"pull request development attention Git reader is unavailable",
		)
	}
	return reader.reader.SnapshotPinnedLineReview(ctx, request)
}

// newPRDevelopmentAttentionRuntimeAcquire pins the exact runtime generation
// and returns only the requested repair owner's durable session store.
func newPRDevelopmentAttentionRuntimeAcquire(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) prdevelopment.AttentionContextRuntimeAcquire {
	if cfg == nil || agentLoop == nil {
		return nil
	}
	return func(
		ctx context.Context,
		agentID string,
	) (context.Context, session.SessionStore, func(), error) {
		if !routing.IsCanonicalAgentID(agentID) || strings.TrimSpace(agentID) != agentID {
			return ctx, nil, func() {}, fmt.Errorf(
				"pull request development attention owner agent ID is not exact and canonical",
			)
		}
		leaseCtx, release, err := agentLoop.AcquireRuntimeGeneration(ctx, cfg)
		if err != nil {
			return ctx, nil, func() {}, err
		}
		registry := agentLoop.GetRegistry()
		if registry == nil {
			release()
			return ctx, nil, func() {}, fmt.Errorf(
				"pull request development attention agent registry is not configured",
			)
		}
		runtimeAgent, ok := registry.GetAgent(agentID)
		if !ok || runtimeAgent == nil || runtimeAgent.ID != agentID {
			release()
			return ctx, nil, func() {}, fmt.Errorf(
				"pull request development attention owner agent %q is unavailable",
				agentID,
			)
		}
		// A session store is required only by ai_working_context. Isolated and
		// deterministic mixtures still need this generation lease for the exact
		// Git projection, but must not depend on or receive a session capability.
		return leaseCtx, runtimeAgent.Sessions, release, nil
	}
}
