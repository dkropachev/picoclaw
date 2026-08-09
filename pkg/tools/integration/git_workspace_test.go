package integrationtools

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestGitWorkspaceToolAcquireUsesSessionContext(t *testing.T) {
	manager := &fakeGitWorkspaceManager{
		acquireInfo: gitworkspace.WorkspaceInfo{
			ID:        "gw-test",
			RemoteURL: "https://example.test/repo.git",
			Path:      "/tmp/workspace",
			Status:    "locked",
		},
	}
	tool := NewGitWorkspaceTool(manager)
	ctx := WithToolSessionContext(context.Background(), "main-agent", "session-123", nil)

	result := tool.Execute(ctx, map[string]any{
		"action":     "acquire",
		"repository": "https://example.test/repo.git",
		"ref":        "feature",
	})

	if result.IsError {
		t.Fatalf("Execute(acquire) returned error: %s", result.ForLLM)
	}
	if manager.acquireReq.Repository != "https://example.test/repo.git" {
		t.Fatalf("Acquire repository = %q", manager.acquireReq.Repository)
	}
	if manager.acquireReq.Ref != "feature" {
		t.Fatalf("Acquire ref = %q", manager.acquireReq.Ref)
	}
	if manager.acquireReq.SessionKey != "session-123" ||
		manager.acquireReq.AgentID != "main-agent" {
		t.Fatalf(
			"Acquire session/agent = %q/%q",
			manager.acquireReq.SessionKey,
			manager.acquireReq.AgentID,
		)
	}
	var payload struct {
		Workspace gitworkspace.WorkspaceInfo `json:"workspace"`
		Next      string                     `json:"next"`
	}
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload.Workspace.ID != "gw-test" || payload.Next == "" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGitWorkspaceToolReleaseUsesSessionContext(t *testing.T) {
	manager := &fakeGitWorkspaceManager{
		released: []gitworkspace.WorkspaceInfo{{ID: "gw-test", Status: "available"}},
	}
	tool := NewGitWorkspaceTool(manager)
	ctx := WithToolSessionContext(context.Background(), "main-agent", "session-123", nil)

	result := tool.Execute(ctx, map[string]any{"action": "release"})

	if result.IsError {
		t.Fatalf("Execute(release) returned error: %s", result.ForLLM)
	}
	if manager.releaseReq.SessionKey != "session-123" ||
		manager.releaseReq.AgentID != "main-agent" {
		t.Fatalf(
			"Release session/agent = %q/%q",
			manager.releaseReq.SessionKey,
			manager.releaseReq.AgentID,
		)
	}
	var payload struct {
		Released []gitworkspace.WorkspaceInfo `json:"released"`
	}
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(payload.Released) != 1 || payload.Released[0].ID != "gw-test" {
		t.Fatalf("released payload = %+v", payload.Released)
	}
}

func TestGitWorkspaceToolDoesNotExposePinnedControllerOperations(t *testing.T) {
	manager := &fakeGitWorkspaceManager{}
	tool := NewGitWorkspaceTool(manager)
	properties, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("tool properties are unavailable")
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatal("tool action schema is unavailable")
	}
	actions, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("tool action enum is unavailable")
	}
	for _, forbidden := range []string{
		"acquire_pinned",
		"lock_pinned",
		"snapshot_pinned",
		"commit_pinned",
		"release_pinned",
	} {
		if slices.Contains(actions, forbidden) {
			t.Fatalf("controller-only action %q appears in tool schema", forbidden)
		}
	}
	for _, forbidden := range []string{"expected_commit", "reservation_key", "source_ref"} {
		if _, found := properties[forbidden]; found {
			t.Fatalf("controller-only property %q appears in tool schema", forbidden)
		}
	}
	result := tool.Execute(context.Background(), map[string]any{"action": "acquire_pinned"})
	if !result.IsError || manager.acquireReq.Repository != "" {
		t.Fatalf("controller-only tool execution result = %#v, acquire = %#v", result, manager.acquireReq)
	}
}

func TestGitWorkspaceToolStatusFiltersWorkspace(t *testing.T) {
	manager := &fakeGitWorkspaceManager{
		stats: gitworkspace.Stats{
			Workspaces: []gitworkspace.WorkspaceInfo{
				{ID: "one", Status: "available"},
				{ID: "two", Status: "locked"},
			},
		},
	}
	tool := NewGitWorkspaceTool(manager)

	result := tool.Execute(context.Background(), map[string]any{
		"action":       "status",
		"workspace_id": "two",
	})

	if result.IsError {
		t.Fatalf("Execute(status) returned error: %s", result.ForLLM)
	}
	var workspace gitworkspace.WorkspaceInfo
	if err := json.Unmarshal([]byte(result.ForLLM), &workspace); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if workspace.ID != "two" || workspace.Status != "locked" {
		t.Fatalf("workspace = %+v", workspace)
	}
}

type fakeGitWorkspaceManager struct {
	acquireReq  gitworkspace.AcquireRequest
	releaseReq  gitworkspace.ReleaseRequest
	acquireInfo gitworkspace.WorkspaceInfo
	released    []gitworkspace.WorkspaceInfo
	stats       gitworkspace.Stats
}

func (f *fakeGitWorkspaceManager) Acquire(
	ctx context.Context,
	req gitworkspace.AcquireRequest,
) (gitworkspace.WorkspaceInfo, error) {
	f.acquireReq = req
	return f.acquireInfo, nil
}

func (f *fakeGitWorkspaceManager) ReleaseSession(
	ctx context.Context,
	req gitworkspace.ReleaseRequest,
) ([]gitworkspace.WorkspaceInfo, error) {
	f.releaseReq = req
	return f.released, nil
}

func (f *fakeGitWorkspaceManager) Stats(ctx context.Context) (gitworkspace.Stats, error) {
	return f.stats, nil
}

func (f *fakeGitWorkspaceManager) CleanupIgnored(
	ctx context.Context,
	workspaceID string,
) (gitworkspace.CleanupResult, error) {
	return gitworkspace.CleanupResult{}, nil
}

func (f *fakeGitWorkspaceManager) Drop(
	ctx context.Context,
	workspaceID string,
) (gitworkspace.WorkspaceInfo, error) {
	return gitworkspace.WorkspaceInfo{}, nil
}

func (f *fakeGitWorkspaceManager) Reconcile(
	ctx context.Context,
) (gitworkspace.ReconcileResult, error) {
	return gitworkspace.ReconcileResult{}, nil
}
