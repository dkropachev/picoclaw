package agent

import (
	"context"
	"errors"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// ControllerGitWorkspaceManager returns the concrete production workspace
// manager for trusted PR-development controller orchestration. It deliberately
// rejects alternate gitWorkspaceManager implementations, so it is not a tool or
// workflow extension point and does not broaden the model-facing capability
// surface.
//
// The caller must hold an AcquireRuntimeGeneration lease while obtaining and
// using the returned manager. This method does not acquire or retain that lease
// itself.
func (al *AgentLoop) ControllerGitWorkspaceManager() (*gitworkspace.Manager, error) {
	if al == nil {
		return nil, errors.New("controller git workspace manager is unavailable")
	}

	al.mu.RLock()
	manager, ok := al.gitWorkspaces.(*gitworkspace.Manager)
	al.mu.RUnlock()
	if !ok || manager == nil {
		return nil, errors.New("controller git workspace manager is unavailable")
	}
	return manager, nil
}

type gitWorkspaceManager interface {
	Acquire(ctx context.Context, req gitworkspace.AcquireRequest) (gitworkspace.WorkspaceInfo, error)
	WithPinnedOperation(
		ctx context.Context,
		req gitworkspace.PinnedAcquireRequest,
		run func(context.Context) error,
	) error
	AcquirePinned(ctx context.Context, req gitworkspace.PinnedAcquireRequest) (gitworkspace.WorkspaceInfo, error)
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
		logger.WarnSafeCF(
			logger.ComponentGitWorkspace,
			logger.DiagnosticMessageGitWorkspaceFailedToInitializeGitWorkspaceManager,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
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
		logger.WarnSafeCF(
			logger.ComponentGitWorkspace,
			logger.DiagnosticMessageGitWorkspaceFailedToReleaseGitWorkspaceLocks,
			logger.NewSafeFields(
				agentDiagnosticSessionField(ts.sessionKey),
				agentDiagnosticAgentField(ts.agentID),
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
		return
	}
	if len(released) == 0 {
		return
	}
	if _, err := al.gitWorkspaces.Reconcile(ctx); err != nil {
		logger.WarnSafeCF(
			logger.ComponentGitWorkspace,
			logger.DiagnosticMessageGitWorkspaceFailedToReconcileGitWorkspaceRetention,
			logger.NewSafeFields(
				agentDiagnosticSessionField(ts.sessionKey),
				agentDiagnosticAgentField(ts.agentID),
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
	}
}
