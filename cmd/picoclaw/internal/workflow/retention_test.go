package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestCLIWorkflowRetentionPreservesReviewAttentionRuns(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.RetentionDays = 1
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	t.Setenv(config.EnvConfig, configPath)

	store := workflows.NewFileRunStore(workspace)
	old := time.Now().UTC().Add(-72 * time.Hour)
	attention := &workflows.Run{
		ID:          "wr_55555555555555555555555555555555",
		WorkflowRef: "inline/review-attention-gates/v1",
		Status:      workflows.RunStatusSucceeded,
		CreatedAt:   old,
		UpdatedAt:   old,
		CompletedAt: &old,
	}
	ordinary := &workflows.Run{
		ID:          "wr_66666666666666666666666666666666",
		WorkflowRef: "workflows/expired-cli.yml",
		Status:      workflows.RunStatusCanceled,
		CreatedAt:   old,
		UpdatedAt:   old,
		CompletedAt: &old,
	}
	for _, run := range []*workflows.Run{attention, ordinary} {
		if err := store.CreateRun(context.Background(), run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
	if _, err := workflowRunStore(context.Background()); err != nil {
		t.Fatalf("workflowRunStore() error = %v", err)
	}
	if _, err := store.GetRun(context.Background(), attention.ID); err != nil {
		t.Fatalf("retained attention GetRun() error = %v", err)
	}
	if _, err := store.GetRun(context.Background(), ordinary.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired ordinary GetRun() error = %v, want not exist", err)
	}
}
