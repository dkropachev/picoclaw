package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestLoadScheduledWorkflowRunsSkipsUntilRevalidated(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowAutomationFile(t, workspace, "scheduled.yml", `
name: Scheduled
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	al := newWorkflowAutomationTestLoop(workspace)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	schedules, err := al.loadScheduledWorkflowRuns(ctx, workspace, now, nil)
	if err != nil {
		t.Fatalf("loadScheduledWorkflowRuns() error = %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("schedules before revalidation = %#v, want none", schedules)
	}

	if _, revalidateErr := workflows.RevalidateLocal(
		ctx,
		workspace,
		workflowRuntimeCompatibility(),
	); revalidateErr != nil {
		t.Fatalf("RevalidateLocal() error = %v", revalidateErr)
	}
	schedules, err = al.loadScheduledWorkflowRuns(ctx, workspace, now, nil)
	if err != nil {
		t.Fatalf("loadScheduledWorkflowRuns() after revalidation error = %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules after revalidation = %#v, want one schedule", schedules)
	}
}

func TestHandleWorkflowRuntimeEventSkipsUntilRevalidated(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowAutomationFile(t, workspace, "runtime.yml", `
name: Runtime
on:
  runtime_event:
    kinds: agent.turn.end
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	al := newWorkflowAutomationTestLoop(workspace)
	evt := runtimeevents.Event{
		Kind: runtimeevents.KindAgentTurnEnd,
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
		Scope: runtimeevents.Scope{
			AgentID:    "main",
			SessionKey: "agent:main:test",
		},
	}

	al.handleWorkflowRuntimeEvent(ctx, evt)
	assertNoWorkflowRunsWithin(t, workspace, 200*time.Millisecond)

	if _, err := workflows.RevalidateLocal(ctx, workspace, workflowRuntimeCompatibility()); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	al.handleWorkflowRuntimeEvent(ctx, evt)
	run := waitForWorkflowRun(t, workspace)
	if run.WorkflowRef != "workflows/runtime.yml" {
		t.Fatalf("workflow ref = %q, want workflows/runtime.yml", run.WorkflowRef)
	}
}

func newWorkflowAutomationTestLoop(workspace string) *AgentLoop {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "mock-model",
				MaxTokens: 4096,
			},
		},
		Workflows: config.WorkflowsConfig{
			Enabled: true,
		},
	}
	return NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
}

func writeWorkflowAutomationFile(t *testing.T, workspace, name, contents string) {
	t.Helper()
	dir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func assertNoWorkflowRunsWithin(t *testing.T, workspace string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	store := workflows.NewFileRunStore(workspace)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(context.Background())
		if err != nil {
			t.Fatalf("ListRuns() error = %v", err)
		}
		if len(runs) > 0 {
			t.Fatalf("runs before revalidation = %#v, want none", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWorkflowRun(t *testing.T, workspace string) *workflows.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	store := workflows.NewFileRunStore(workspace)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(context.Background())
		if err != nil {
			t.Fatalf("ListRuns() error = %v", err)
		}
		if len(runs) > 0 {
			run := runs[0]
			return &run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workflow run")
	return nil
}
