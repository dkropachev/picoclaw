package prworkspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

// WorkflowAIRunner adapts the existing agent runtime to the workspace's
// capability-free AI boundary. Every invocation is private, ephemeral,
// history-free, cache-free, tool-free, and schema checked.
type WorkflowAIRunner struct {
	Runner  workflows.AgentRunner
	AgentID string
}

func (runner WorkflowAIRunner) RunIsolated(ctx context.Context, request IsolatedAIRequest) (map[string]any, error) {
	if runner.Runner == nil || request.SystemPrompt == "" || request.UserPrompt == "" || len(request.Schema) == 0 {
		return nil, errors.New("isolated PR workspace AI request is incomplete")
	}
	agentRequest := workflows.AgentRequest{
		AgentID: runner.AgentID, Message: request.UserPrompt,
		EphemeralSession: true, History: "none", Cache: "none",
		Tools: workflows.AgentToolsNone, PrivateContext: true,
		IsolatedSystemPrompt: request.SystemPrompt, Managed: "off",
		Output: &workflows.AgentOutputContract{
			Format: "json", Schema: request.Schema, RepairAttempts: 2,
		},
	}
	if request.SourceExecutionID != "" {
		agentRequest.SourceCapture = &workflows.AgentSourceCapture{
			ExecutionID: request.SourceExecutionID,
			WorkspaceID: request.SourceWorkspaceID,
			Binding:     request.SourceBinding,
		}
	}
	output, err := runner.Runner.RunAgent(ctx, agentRequest)
	if err != nil {
		return nil, err
	}
	structured, ok := output["structured"].(map[string]any)
	if !ok || structured == nil {
		return nil, fmt.Errorf("isolated PR workspace AI returned no structured object")
	}
	if request.SourceExecutionID != "" {
		source, sourceErr := workflowAISource(output)
		if sourceErr != nil {
			return nil, sourceErr
		}
		structured["__source-execution"] = source
	}
	return structured, nil
}

func workflowAISource(output map[string]any) (map[string]any, error) {
	keys := []string{
		"source_execution_id", "source_workspace_id", "source_binding",
		"source_agent_id", "source_session", "source_revision", "source_tools",
	}
	result := make(map[string]any, len(keys))
	for _, key := range keys {
		value, ok := output[key].(string)
		if !ok || value == "" {
			return nil, errors.New("isolated PR workspace AI returned incomplete source provenance")
		}
		result[key] = value
	}
	return result, nil
}

var _ IsolatedAIRunner = WorkflowAIRunner{}
