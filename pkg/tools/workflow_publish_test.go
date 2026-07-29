package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowToolPublishRootYAML = `name: Publish root
on:
  manual: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`

const workflowToolPublishReusableRootYAML = `name: Publish reusable root
on:
  manual: {}
jobs:
  shared:
    uses: workflows/shared.yml
`

const workflowToolPublishReusableChildYAML = `name: Shared
on:
  workflow_call: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`

func TestWorkflowToolDevelopmentPublishRejectsUnavailableGate(t *testing.T) {
	ctx := context.Background()
	workspace, configured, session := prepareWorkflowToolPublish(
		t,
		workflowToolPublishRootYAML,
	)
	unconfigured := NewWorkflowTool(configured.executor, workspace, configured.runtime)

	result := unconfigured.Execute(ctx, map[string]any{"action": "dev_publish"})
	if result == nil || !result.IsError ||
		!errors.Is(result.Err, workflows.ErrWorkflowDevelopmentPublishGateRequired) {
		t.Fatalf("dev_publish result = %#v, want gate-required error", result)
	}
	assertWorkflowToolPublishRejected(t, workspace, session)
}

func TestWorkflowToolDevelopmentPublishRejectsNonReadyDependency(t *testing.T) {
	workspace, tool, session := prepareWorkflowToolPublish(
		t,
		workflowToolPublishRootYAML,
	)
	tool.ConfigureDevelopmentPublishGate(WorkflowDevelopmentPublishGateConfig{
		WorkflowsEnabled: true,
		DefinitionsDir:   workflows.DefaultDefinitionsDir,
		MaxCallDepth:     4,
		Resolver: workflows.WorkflowDependencyRuntimeResolverFunc(func(
			context.Context,
			workflows.WorkflowDependencyOccurrence,
		) workflows.WorkflowDependencyReadinessCode {
			return workflows.WorkflowDependencyReadinessNotFound
		}),
	})

	result := tool.Execute(context.Background(), map[string]any{"action": "dev_publish"})
	if result == nil || !result.IsError ||
		!errors.Is(result.Err, workflows.ErrWorkflowDevelopmentPublishNotReady) {
		t.Fatalf("dev_publish result = %#v, want dependencies-not-ready error", result)
	}
	assertWorkflowToolPublishRejected(t, workspace, session)
}

func TestWorkflowToolDevelopmentPublishFencesReachableReusableContent(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowToolFile(
		t,
		workspace,
		"shared.yml",
		workflowToolPublishReusableChildYAML,
	)
	runtime := workflows.RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "abc123",
	}
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspace,
		runtime,
	); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	tool := newNoopWorkflowTool(
		t,
		workspace,
		runtime,
		workflows.NewFileRunStore(workspace),
	)
	session := prepareWorkflowToolPublishWithTool(
		t,
		tool,
		workflowToolPublishReusableRootYAML,
	)

	var mutate sync.Once
	tool.ConfigureDevelopmentPublishGate(WorkflowDevelopmentPublishGateConfig{
		WorkflowsEnabled: true,
		DefinitionsDir:   workflows.DefaultDefinitionsDir,
		MaxCallDepth:     4,
		Resolver: workflows.WorkflowDependencyRuntimeResolverFunc(func(
			context.Context,
			workflows.WorkflowDependencyOccurrence,
		) workflows.WorkflowDependencyReadinessCode {
			mutate.Do(func() {
				changed := strings.Replace(
					workflowToolPublishReusableChildYAML,
					"name: Shared",
					"name: Changed shared",
					1,
				)
				if err := os.WriteFile(
					filepath.Join(workspace, "workflows", "shared.yml"),
					[]byte(changed),
					0o644,
				); err != nil {
					t.Fatalf("WriteFile(shared) error = %v", err)
				}
			})
			return workflows.WorkflowDependencyReadinessReady
		}),
	})

	result := tool.Execute(context.Background(), map[string]any{"action": "dev_publish"})
	if result == nil || !result.IsError ||
		!errors.Is(result.Err, workflows.ErrWorkflowDevelopmentDependencyRevisionMismatch) {
		t.Fatalf("dev_publish result = %#v, want dependency-revision error", result)
	}
	assertWorkflowToolPublishRejected(t, workspace, session)
}

func prepareWorkflowToolPublish(
	t *testing.T,
	raw string,
) (string, *WorkflowTool, *workflows.WorkflowDevelopmentSession) {
	t.Helper()
	workspace := t.TempDir()
	runtime := workflows.RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "abc123",
	}
	tool := newNoopWorkflowTool(
		t,
		workspace,
		runtime,
		workflows.NewFileRunStore(workspace),
	)
	return workspace, tool, prepareWorkflowToolPublishWithTool(t, tool, raw)
}

func prepareWorkflowToolPublishWithTool(
	t *testing.T,
	tool *WorkflowTool,
	raw string,
) *workflows.WorkflowDevelopmentSession {
	t.Helper()
	ctx := context.Background()
	for _, args := range []map[string]any{
		{
			"action":     "dev_start",
			"target_ref": "workflows/publish.yml",
			"prompt":     "prepare publish test",
		},
		{
			"action": "dev_revise",
			"yaml":   raw,
		},
		{
			"action": "dev_test",
		},
	} {
		result := tool.Execute(ctx, args)
		if result == nil || result.IsError {
			t.Fatalf("%s result = %#v", args["action"], result)
		}
	}
	session, err := workflows.GetWorkflowDevelopmentSession(tool.workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if session == nil ||
		session.Status != workflows.WorkflowDevelopmentStatusReadyToPublish {
		t.Fatalf("session = %#v, want ready-to-publish", session)
	}
	return session
}

func assertWorkflowToolPublishRejected(
	t *testing.T,
	workspace string,
	expected *workflows.WorkflowDevelopmentSession,
) {
	t.Helper()
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active == nil || active.SessionRevision != expected.SessionRevision {
		t.Fatalf("active session = %#v, want revision %q", active, expected.SessionRevision)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workflows", "publish.yml")); !os.IsNotExist(err) {
		t.Fatalf("published target stat error = %v, want missing", err)
	}
}
