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

func (runner WorkflowAIRunner) RunIsolated(ctx context.Context, request IsolatedAIRequest) (IsolatedAIResult, error) {
	if runner.Runner == nil || request.SystemPrompt == "" || request.UserPrompt == "" || len(request.Schema) == 0 {
		return IsolatedAIResult{}, errors.New("isolated PR workspace AI request is incomplete")
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
	usage, usageErr := workflowAIUsage(output)
	usageComplete, completeOK := false, false
	if output != nil {
		usageComplete, completeOK = output["usage_complete"].(bool)
	}
	if !completeOK {
		usageErr = errors.Join(
			usageErr,
			errors.New("isolated PR workspace AI returned no usage completeness"),
		)
	}
	result := IsolatedAIResult{
		Usage: usage,
		Complete: usageErr == nil && usageComplete && usage.ProviderCalls > 0 &&
			usage.ProviderCalls == usage.UsageReportedCalls,
	}
	if usageErr != nil {
		return result, errors.Join(err, usageErr)
	}
	if err != nil {
		return result, err
	}
	structured, ok := output["structured"].(map[string]any)
	if !ok || structured == nil {
		return result, fmt.Errorf("isolated PR workspace AI returned no structured object")
	}
	if request.SourceExecutionID != "" {
		source, sourceErr := workflowAISource(output)
		if sourceErr != nil {
			return result, sourceErr
		}
		structured["__source-execution"] = source
	}
	result.Structured = structured
	return result, nil
}

func workflowAIUsage(output map[string]any) (TokenUsage, error) {
	if output == nil {
		return TokenUsage{}, nil
	}
	raw, exists := output["usage"]
	if !exists {
		return TokenUsage{}, errors.New("isolated PR workspace AI returned no usage")
	}
	values, ok := raw.([]workflows.AgentUsage)
	if !ok {
		return TokenUsage{}, errors.New("isolated PR workspace AI returned malformed usage")
	}
	var total TokenUsage
	for _, value := range values {
		measurement := TokenUsage{
			ProviderCalls: value.ProviderCalls, UsageReportedCalls: value.UsageReportedCalls,
			PromptTokens: int64(value.PromptTokens), CachedTokens: int64(value.CachedTokens),
			CompletionTokens: int64(value.CompletionTokens), ReasoningTokens: int64(value.ReasoningTokens),
			TotalTokens: int64(value.TotalTokens), LatencyMillis: value.LatencyMillis,
		}
		next, err := AddTokenUsage(total, measurement)
		if err != nil {
			return total, fmt.Errorf("isolated PR workspace AI returned invalid usage: %w", err)
		}
		total = next
	}
	return total, nil
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
