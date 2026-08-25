package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

func TestCodexExecCommandTool_MapsCommandToExecBackend(t *testing.T) {
	execTool, err := NewExecToolWithConfig(t.TempDir(), false, &config.Config{
		Tools: config.ToolsConfig{
			Exec: config.ExecConfig{
				ToolConfig:     config.ToolConfig{Enabled: true},
				AllowRemote:    true,
				TimeoutSeconds: 5,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	result := NewCodexExecCommandTool(execTool).Execute(context.Background(), map[string]any{
		"cmd": "printf codex-compatible",
	})
	if result.IsError {
		t.Fatalf("exec_command returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "codex-compatible") {
		t.Fatalf("ForLLM = %q, want command output", result.ForLLM)
	}
}

func TestUpdatePlanTool_RejectsMultipleInProgressItems(t *testing.T) {
	result := NewUpdatePlanTool().Execute(context.Background(), map[string]any{
		"plan": []any{
			map[string]any{"step": "first", "status": "in_progress"},
			map[string]any{"step": "second", "status": "in_progress"},
		},
	})
	if !result.IsError {
		t.Fatalf("expected multiple in_progress items to fail, got: %s", result.ForLLM)
	}
}

func TestCodexViewImageTool_ForwardsPathToLoader(t *testing.T) {
	loader := &mockRegistryTool{
		name:   "load_image",
		desc:   "load",
		params: map[string]any{"type": "object"},
		result: SilentResult("loaded"),
	}

	result := NewCodexViewImageTool(loader).Execute(context.Background(), map[string]any{
		"path":   "image.png",
		"detail": "high",
	})
	if result.IsError {
		t.Fatalf("view_image returned error: %s", result.ForLLM)
	}
	if result.ForLLM != "loaded" {
		t.Fatalf("ForLLM = %q, want loaded", result.ForLLM)
	}
}

func TestCodexViewImageTool_ForwardsMediaStoreToPrivateLoader(t *testing.T) {
	loader := &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("load_image", "load"),
	}
	store := media.NewFileMediaStore()
	NewCodexViewImageTool(loader).SetMediaStore(store)
	if loader.store != store {
		t.Fatal("view_image did not forward media-store injection to its loader")
	}

	NewCodexViewImageTool(newMockTool("load_image", "load")).SetMediaStore(store)
	var nilView *CodexViewImageTool
	nilView.SetMediaStore(store)
}
