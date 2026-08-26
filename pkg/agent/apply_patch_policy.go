package agent

import (
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
)

// agentApplyPatchProtectedRoots returns only control paths whose locations are
// authoritative at AgentInstance construction. Other runtime owners inject
// their own exact roots through the apply-patch preflight policy when they
// construct a tool.
func agentApplyPatchProtectedRoots(workspace string, cfg *config.Config) []string {
	roots := []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "account_router_state.json"),
	}
	if cfg != nil {
		roots = append(roots, cfg.GitWorkspaceRootPath())
	}
	return roots
}
