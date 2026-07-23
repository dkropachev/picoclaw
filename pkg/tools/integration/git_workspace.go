package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type GitWorkspaceManager interface {
	Acquire(ctx context.Context, req gitworkspace.AcquireRequest) (gitworkspace.WorkspaceInfo, error)
	ReleaseSession(ctx context.Context, req gitworkspace.ReleaseRequest) ([]gitworkspace.WorkspaceInfo, error)
	Stats(ctx context.Context) (gitworkspace.Stats, error)
	CleanupIgnored(ctx context.Context, workspaceID string) (gitworkspace.CleanupResult, error)
	Drop(ctx context.Context, workspaceID string) (gitworkspace.WorkspaceInfo, error)
	Reconcile(ctx context.Context) (gitworkspace.ReconcileResult, error)
}

type GitWorkspaceTool struct {
	manager GitWorkspaceManager
}

func NewGitWorkspaceTool(manager GitWorkspaceManager) *GitWorkspaceTool {
	return &GitWorkspaceTool{manager: manager}
}

func (t *GitWorkspaceTool) Name() string {
	return "git_workspace"
}

func (t *GitWorkspaceTool) Description() string {
	return "Allocate, lock, inspect, release, clean, and drop reusable local git repository workspaces. Use acquire before working in a checked out repository, then use the returned path with file and exec tools."
}

func (t *GitWorkspaceTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"acquire", "list", "status", "release", "clean_ignored", "drop", "reconcile"},
				"description": "Action to perform. acquire locks a checkout for this session; release preserves changes and unlocks this session's checkout; clean_ignored removes ignored files from an unlocked workspace; drop removes an unlocked local checkout after preserving changes.",
			},
			"repository": map[string]any{
				"type":        "string",
				"description": "Git remote URL or local repository path. Required for acquire.",
			},
			"ref": map[string]any{
				"type":        "string",
				"description": "Optional branch, tag, or commit to check out when a new workspace is cloned.",
			},
			"workspace_id": map[string]any{
				"type":        "string",
				"description": "Workspace ID for status, clean_ignored, or drop.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GitWorkspaceTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.manager == nil {
		return ErrorResult("git workspace manager is not configured")
	}
	action, _ := args["action"].(string)
	action = strings.TrimSpace(action)
	switch action {
	case "acquire":
		repository, _ := args["repository"].(string)
		ref, _ := args["ref"].(string)
		info, err := t.manager.Acquire(ctx, gitworkspace.AcquireRequest{
			Repository: repository,
			Ref:        ref,
			SessionKey: ToolSessionKey(ctx),
			AgentID:    ToolAgentID(ctx),
		})
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(map[string]any{
			"workspace": info,
			"next":      "Use workspace.path as cwd for exec, or pass paths under it to file tools.",
		})
	case "list":
		stats, err := t.manager.Stats(ctx)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(stats)
	case "status":
		stats, err := t.manager.Stats(ctx)
		if err != nil {
			return ErrorResult(err.Error())
		}
		workspaceID, _ := args["workspace_id"].(string)
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID == "" {
			return jsonToolResult(stats)
		}
		for _, ws := range stats.Workspaces {
			if ws.ID == workspaceID {
				return jsonToolResult(ws)
			}
		}
		return ErrorResult(fmt.Sprintf("git workspace %q not found", workspaceID))
	case "release":
		released, err := t.manager.ReleaseSession(ctx, gitworkspace.ReleaseRequest{
			SessionKey: ToolSessionKey(ctx),
			AgentID:    ToolAgentID(ctx),
		})
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(map[string]any{"released": released})
	case "clean_ignored":
		workspaceID, _ := args["workspace_id"].(string)
		result, err := t.manager.CleanupIgnored(ctx, workspaceID)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(result)
	case "drop":
		workspaceID, _ := args["workspace_id"].(string)
		info, err := t.manager.Drop(ctx, workspaceID)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(map[string]any{"dropped": info})
	case "reconcile":
		result, err := t.manager.Reconcile(ctx)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return jsonToolResult(result)
	default:
		return ErrorResult("unsupported git_workspace action")
	}
}

func jsonToolResult(value any) *ToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(string(data))
}
