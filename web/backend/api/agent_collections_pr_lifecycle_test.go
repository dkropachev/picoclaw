package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const nestedAgentDeleteActionWorkflow = `
name: Nested agent delete action
gates:
  inspect:
    prompt: Inspect the frozen subject.
    fields:
      - id: confirmed
        type: boolean
        label: Confirmed
        required: true
    default-action:
      type: ai
      agent-id: reviewer
      prompt: Inspect the subject and complete the gate fields.
      session: ephemeral
      history: none
      cache: none
      tools: none
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.inspect.outputs.field-values }}
    steps:
      - id: inspect
        uses: gate/exec
        with: {gate-ref: gates.inspect}
`

const rootAgentDeleteActionWorkflow = `
name: Root agent delete action
gates:
  delegate:
    prompt: Delegate inspection.
    fields:
      - id: confirmed
        type: boolean
        label: Confirmed
        required: true
    default-action:
      type: workflow
      workflow-ref: workflows/gate-actions/nested.yml
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.delegate.outputs.field-values }}
    steps:
      - id: delegate
        uses: gate/exec
        with: {gate-ref: gates.delegate}
`

func TestAgentDeletesReportPRLifecycleReferencesWithPartialSuccess(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "reviewer"},
			{ID: "worker"},
		}
		workflow := cfg.PRLifecycle.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
		workflow.Name = "Automated"
		workflow.Bindings = append(workflow.Bindings, config.PRLifecycleGateBinding{
			WorkflowRef: config.PRLifecycleWorkflowRef,
			GateRef:     "gates.charter-confirm",
			Action: &gatetypes.GateAction{
				Type: gatetypes.GateActionAI, AgentID: "reviewer",
				Prompt:  "Review the charter and complete the gate fields.",
				Session: "ephemeral", History: "none", Cache: "none", Tools: "none",
			},
		})
		cfg.PRLifecycle.WorkflowConfigurations["automated"] = workflow
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	bulk := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
		"ids": []string{"reviewer", "worker"}, "config_revision": revision,
	})
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulk.Code, bulk.Body.String())
	}
	var result agentBulkDeleteResponse
	if decodeErr := json.Unmarshal(bulk.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "worker" ||
		len(result.Failures) != 1 || result.Failures[0].ID != "reviewer" ||
		result.Failures[0].Code != "agent_referenced" ||
		len(result.Failures[0].Blockers) != 1 ||
		result.Failures[0].Blockers[0].Kind != "pr_lifecycle_action" {
		t.Fatalf("bulk result=%#v", result)
	}

	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := findConfiguredAgent(loaded, "reviewer"); !exists {
		t.Fatal("PR-lifecycle-referenced agent was deleted")
	}
	if _, exists := findConfiguredAgent(loaded, "worker"); exists {
		t.Fatal("unreferenced agent was retained")
	}

	single := harness.request(t, http.MethodDelete, "/api/agents/reviewer", map[string]any{
		"expected_config_revision": result.ConfigRevision,
	})
	if single.Code != http.StatusConflict {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}
	var singleError agentErrorResponse
	if err := json.Unmarshal(single.Body.Bytes(), &singleError); err != nil {
		t.Fatal(err)
	}
	if singleError.Error != "agent_referenced" || len(singleError.Blockers) != 1 ||
		singleError.Blockers[0].Kind != "pr_lifecycle_action" {
		t.Fatalf("single delete error=%#v", singleError)
	}
}

func TestAgentDeletesReportNestedPRLifecycleWorkflowReferences(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "reviewer"},
			{ID: "worker"},
		}
		actionDir := filepath.Join(cfg.WorkspacePath(), "workflows", "gate-actions")
		if err := os.MkdirAll(actionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(actionDir, "root.yml"),
			[]byte(rootAgentDeleteActionWorkflow),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(actionDir, "nested.yml"),
			[]byte(nestedAgentDeleteActionWorkflow),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		workflow := cfg.PRLifecycle.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
		workflow.Name = "Automated"
		workflow.Bindings = append(workflow.Bindings, config.PRLifecycleGateBinding{
			WorkflowRef: config.PRLifecycleWorkflowRef,
			GateRef:     "gates.charter-confirm",
			Action: &gatetypes.GateAction{
				Type:        gatetypes.GateActionWorkflow,
				WorkflowRef: "workflows/gate-actions/root.yml",
			},
		})
		cfg.PRLifecycle.WorkflowConfigurations["automated"] = workflow
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	bulk := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
		"ids": []string{"reviewer", "worker"}, "config_revision": revision,
	})
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulk.Code, bulk.Body.String())
	}
	var result agentBulkDeleteResponse
	if err := json.Unmarshal(bulk.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "worker" ||
		len(result.Failures) != 1 || result.Failures[0].ID != "reviewer" ||
		len(result.Failures[0].Blockers) != 1 ||
		result.Failures[0].Blockers[0].Kind != "pr_lifecycle_action" ||
		result.Failures[0].Blockers[0].Name != "workflows/gate-actions/nested.yml:inspect" {
		t.Fatalf("bulk result=%#v", result)
	}
}

func TestAgentDeleteFailsClosedForCyclicPRLifecycleActionWorkflow(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}, {ID: "worker"}}
		actionDir := filepath.Join(cfg.WorkspacePath(), "workflows", "gate-actions")
		if err := os.MkdirAll(actionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cyclic := strings.ReplaceAll(
			rootAgentDeleteActionWorkflow,
			"workflows/gate-actions/nested.yml",
			"workflows/gate-actions/root.yml",
		)
		if err := os.WriteFile(filepath.Join(actionDir, "root.yml"), []byte(cyclic), 0o600); err != nil {
			t.Fatal(err)
		}
		workflow := cfg.PRLifecycle.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
		workflow.Name = "Automated"
		workflow.Bindings = append(workflow.Bindings, config.PRLifecycleGateBinding{
			WorkflowRef: config.PRLifecycleWorkflowRef,
			GateRef:     "gates.charter-confirm",
			Action: &gatetypes.GateAction{
				Type:        gatetypes.GateActionWorkflow,
				WorkflowRef: "workflows/gate-actions/root.yml",
			},
		})
		cfg.PRLifecycle.WorkflowConfigurations["automated"] = workflow
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	deleted := harness.request(t, http.MethodDelete, "/api/agents/worker", map[string]any{
		"expected_config_revision": revision,
	})
	if deleted.Code != http.StatusConflict {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	var response agentErrorResponse
	if err := json.Unmarshal(deleted.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "agent_referenced" || len(response.Blockers) != 1 ||
		response.Blockers[0].Kind != "pr_lifecycle_workflow_unavailable" {
		t.Fatalf("delete response=%#v", response)
	}
}
