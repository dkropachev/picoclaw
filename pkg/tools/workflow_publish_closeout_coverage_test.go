package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowPublishCloseoutConfigurationAndGateGuards(t *testing.T) {
	if configured := (*WorkflowTool)(nil).ConfigureDevelopmentPublishGate(
		WorkflowDevelopmentPublishGateConfig{},
	); configured != nil {
		t.Fatalf("nil workflow tool configuration = %#v", configured)
	}
	tool := &WorkflowTool{definitionsDir: "configured-definitions"}
	if configured := tool.ConfigureDevelopmentPublishGate(
		WorkflowDevelopmentPublishGateConfig{},
	); configured != tool || tool.publishGate.DefinitionsDir != "configured-definitions" {
		t.Fatalf("defaulted publish gate = %#v", tool.publishGate)
	}
	if _, err := (&WorkflowTool{}).workflowDevelopmentPublishGate(); !errors.Is(
		err,
		workflows.ErrWorkflowDevelopmentPublishGateRequired,
	) {
		t.Fatalf("missing publish gate = %v", err)
	}

	resolver := workflows.WorkflowDependencyRuntimeResolverFunc(func(
		context.Context,
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return workflows.WorkflowDependencyReadinessReady
	})
	config := WorkflowDevelopmentPublishGateConfig{Resolver: resolver}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evaluateWorkflowDevelopmentPublishGate(
		canceled, t.TempDir(), config, workflows.WorkflowDevelopmentPublishGateInput{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publish gate = %v", err)
	}
	if _, err := evaluateWorkflowDevelopmentPublishGate(
		context.Background(), t.TempDir(), WorkflowDevelopmentPublishGateConfig{},
		workflows.WorkflowDevelopmentPublishGateInput{},
	); !errors.Is(err, workflows.ErrWorkflowDevelopmentPublishGateRequired) {
		t.Fatalf("nil resolver publish gate = %v", err)
	}
	if _, err := evaluateWorkflowDevelopmentPublishGate(
		context.Background(), t.TempDir(), config,
		workflows.WorkflowDevelopmentPublishGateInput{},
	); err == nil {
		t.Fatal("nil parsed workflow publish gate succeeded")
	}
	workflow := &workflows.Workflow{}
	if _, err := evaluateWorkflowDevelopmentPublishGate(
		context.Background(), t.TempDir(), config,
		workflows.WorkflowDevelopmentPublishGateInput{
			Workflow: workflow,
			YAML:     strings.Repeat("x", int(workflows.MaxWorkflowDependencyDefinitionBytes)+1),
		},
	); !errors.Is(err, workflows.ErrWorkflowDependencyAnalysisLimitExceeded) {
		t.Fatalf("oversize draft publish gate = %v", err)
	}
	if _, err := evaluateWorkflowDevelopmentPublishGate(
		context.Background(), t.TempDir(), config,
		workflows.WorkflowDevelopmentPublishGateInput{
			Workflow:    workflow,
			WorkflowRef: "../invalid",
		},
	); err == nil {
		t.Fatal("invalid workflow ref publish gate succeeded")
	}
}

func TestWorkflowPublishCloseoutSnapshotLoaderFailures(t *testing.T) {
	loader := &workflowDevelopmentDependencySnapshotLoader{
		resolver:  workflows.Resolver{WorkspaceDir: t.TempDir(), DefinitionsDir: workflows.DefaultDefinitionsDir},
		snapshots: make(map[string]workflowDevelopmentDependencySnapshot),
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if workflow, err := loader.LoadReusableWorkflow(
		canceled,
		"workflows/child.yml",
	); !errors.Is(err, context.Canceled) || workflow != nil {
		t.Fatalf("canceled reusable load = %#v, %v", workflow, err)
	}
	if workflow, err := loader.LoadReusableWorkflow(context.Background(), "../invalid"); err == nil || workflow != nil {
		t.Fatalf("invalid reusable ref = %#v, %v", workflow, err)
	}
	if workflow, err := loader.LoadReusableWorkflow(
		context.Background(), "workflows/missing.yml",
	); err == nil || workflow != nil {
		t.Fatalf("missing reusable workflow = %#v, %v", workflow, err)
	}
	loader.resolver.DefinitionsDir = "/absolute"
	if workflow, err := loader.LoadReusableWorkflow(
		context.Background(), "workflows/missing.yml",
	); err == nil || workflow != nil {
		t.Fatalf("invalid resolver definition root = %#v, %v", workflow, err)
	}

	workspace := t.TempDir()
	definitions := filepath.Join(workspace, workflows.DefaultDefinitionsDir)
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(definitions, "invalid.yml"), []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader = &workflowDevelopmentDependencySnapshotLoader{
		resolver: workflows.Resolver{
			WorkspaceDir: workspace, DefinitionsDir: workflows.DefaultDefinitionsDir,
		},
		snapshots: make(map[string]workflowDevelopmentDependencySnapshot),
	}
	if workflow, err := loader.LoadReusableWorkflow(
		context.Background(), "workflows/invalid.yml",
	); err == nil || workflow != nil {
		t.Fatalf("invalid reusable definition = %#v, %v", workflow, err)
	}
}

func TestWorkflowPublishCloseoutDefaultDefinitionsGate(t *testing.T) {
	parsed, err := workflows.Parse([]byte(workflowToolPublishRootYAML))
	if err != nil {
		t.Fatal(err)
	}
	resolver := workflows.WorkflowDependencyRuntimeResolverFunc(func(
		context.Context,
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return workflows.WorkflowDependencyReadinessReady
	})
	result, err := evaluateWorkflowDevelopmentPublishGate(
		context.Background(),
		t.TempDir(),
		WorkflowDevelopmentPublishGateConfig{
			WorkflowsEnabled: true,
			Resolver:         resolver,
		},
		workflows.WorkflowDevelopmentPublishGateInput{
			WorkflowRef:   "workflows/root.yml",
			DraftRevision: "draft",
			YAML:          workflowToolPublishRootYAML,
			Workflow:      parsed,
		},
	)
	if err != nil || result.Revision == "" {
		t.Fatalf("default definitions gate = %#v, %v", result, err)
	}
}

func TestWorkflowPublishCloseoutDefinitionReadLimits(t *testing.T) {
	if _, err := readWorkflowDevelopmentDependencyDefinition("missing", 0); !errors.Is(
		err, workflows.ErrWorkflowDependencyAnalysisLimitExceeded,
	) {
		t.Fatalf("zero remaining definition read = %v", err)
	}
	if _, err := readWorkflowDevelopmentDependencyDefinition(
		filepath.Join(t.TempDir(), "missing"), 1,
	); err == nil {
		t.Fatal("missing dependency definition read succeeded")
	}
	directory := t.TempDir()
	if _, err := readWorkflowDevelopmentDependencyDefinition(directory, 10); err == nil {
		t.Fatal("directory dependency definition read succeeded")
	}
	path := filepath.Join(t.TempDir(), "large.yml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 11)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowDevelopmentDependencyDefinition(path, 10); !errors.Is(
		err, workflows.ErrWorkflowDependencyAnalysisLimitExceeded,
	) {
		t.Fatalf("oversize dependency definition read = %v", err)
	}
	if data, err := readWorkflowDevelopmentDependencyDefinition(path, 11); err != nil || len(data) != 11 {
		t.Fatalf("bounded dependency definition = %d bytes, %v", len(data), err)
	}
}

func TestWorkflowPublishCloseoutRevisionUnavailableSnapshot(t *testing.T) {
	revision, err := workflowDevelopmentDependencyRevision(
		workflows.WorkflowDevelopmentPublishGateInput{
			WorkflowRef:   "workflows/root.yml",
			DraftRevision: "draft",
			YAML:          "root",
		},
		map[string]workflowDevelopmentDependencySnapshot{
			"workflows/missing.yml": {ref: "workflows/missing.yml"},
			"workflows/ready.yml": {
				ref: "workflows/ready.yml", available: true, content: []byte("ready"),
			},
		},
		workflowDevelopmentDependencyReport{},
	)
	if err != nil || !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("dependency revision = %q, %v", revision, err)
	}
}
