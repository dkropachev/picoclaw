package agent

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type gitWorkspaceManager interface {
	Acquire(ctx context.Context, req gitworkspace.AcquireRequest) (gitworkspace.WorkspaceInfo, error)
	ReleaseSession(ctx context.Context, req gitworkspace.ReleaseRequest) ([]gitworkspace.WorkspaceInfo, error)
	Stats(ctx context.Context) (gitworkspace.Stats, error)
	CleanupIgnored(ctx context.Context, workspaceID string) (gitworkspace.CleanupResult, error)
	Drop(ctx context.Context, workspaceID string) (gitworkspace.WorkspaceInfo, error)
	Reconcile(ctx context.Context) (gitworkspace.ReconcileResult, error)
}

func newGitWorkspaceManagerFromConfig(cfg *config.Config) gitWorkspaceManager {
	if cfg == nil {
		return nil
	}
	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir:             cfg.GitWorkspaceRootPath(),
		MaxTotalSizeBytes:   cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes(),
		IgnoredCleanupDelay: cfg.GitWorkspaces.EffectiveIgnoredCleanupDelay(),
		DropDelay:           cfg.GitWorkspaces.EffectiveDropDelay(),
	})
	if err != nil {
		logger.WarnCF("git-workspace", "Failed to initialize git workspace manager", map[string]any{
			"error": err.Error(),
		})
		return nil
	}
	return manager
}

func (al *AgentLoop) releaseGitWorkspacesForTurn(ctx context.Context, ts *turnState) {
	if al == nil || al.gitWorkspaces == nil || ts == nil || ts.sessionKey == "" {
		return
	}
	released, err := al.gitWorkspaces.ReleaseSession(ctx, gitworkspace.ReleaseRequest{
		SessionKey: ts.sessionKey,
		AgentID:    ts.agentID,
	})
	if err != nil {
		logger.WarnCF("git-workspace", "Failed to release git workspace locks", map[string]any{
			"session_key": ts.sessionKey,
			"agent_id":    ts.agentID,
			"error":       err.Error(),
		})
		return
	}
	if len(released) == 0 {
		return
	}
	if _, err := al.gitWorkspaces.Reconcile(ctx); err != nil {
		logger.WarnCF("git-workspace", "Failed to reconcile git workspace retention", map[string]any{
			"session_key": ts.sessionKey,
			"agent_id":    ts.agentID,
			"error":       err.Error(),
		})
	}
}
