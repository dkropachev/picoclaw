package prworkspace

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

type fixedWorkflowAgentRunner struct {
	output map[string]any
	err    error
}

func (runner fixedWorkflowAgentRunner) RunAgent(
	context.Context,
	workflows.AgentRequest,
) (map[string]any, error) {
	return runner.output, runner.err
}

type sourceCapturingAgentRunner struct {
	request workflows.AgentRequest
}

func (runner *sourceCapturingAgentRunner) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	runner.request = request
	source := &AIExecutionSource{
		ExecutionID:     request.SourceCapture.ExecutionID,
		WorkspaceID:     request.SourceCapture.WorkspaceID,
		Binding:         request.SourceCapture.Binding,
		AgentID:         request.AgentID,
		SessionRevision: "sha256:source-revision",
		Tools:           workflows.AgentToolsNone,
	}
	source.Session = aiExecutionSourceSessionKey(source)
	return map[string]any{
		"structured":          map[string]any{"summary": "reviewed"},
		"source_execution_id": request.SourceCapture.ExecutionID,
		"source_workspace_id": request.SourceCapture.WorkspaceID,
		"source_binding":      request.SourceCapture.Binding,
		"source_agent_id":     request.AgentID,
		"source_session":      source.Session,
		"source_revision":     "sha256:source-revision",
		"source_tools":        "none",
		"usage": []workflows.AgentUsage{{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
		}},
		"usage_complete": true,
	}, nil
}

func TestWorkflowAIRunnerCapturesPrivateSourceProvenance(t *testing.T) {
	agent := &sourceCapturingAgentRunner{}
	runner := WorkflowAIRunner{Runner: agent, AgentID: "main"}
	value, err := runner.RunIsolated(t.Context(), IsolatedAIRequest{
		Operation: "review.initial", SystemPrompt: "Review privately.",
		UserPrompt: "Review this diff.", Schema: map[string]any{"type": "object"},
		SourceExecutionID: "aix_11111111111111111111111111111111",
		SourceWorkspaceID: "devw_11111111111111111111111111111111",
		SourceBinding:     "sha256:source-binding",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.request.SourceCapture == nil || !agent.request.EphemeralSession ||
		agent.request.Tools != workflows.AgentToolsNone || !agent.request.PrivateContext {
		t.Fatalf("agent request = %#v", agent.request)
	}
	source := aiExecutionSourceFromValue(value.Structured)
	if source == nil || source.AgentID != "main" || source.Session != aiExecutionSourceSessionKey(source) ||
		source.SessionRevision != "sha256:source-revision" {
		t.Fatalf("source = %#v value=%#v", source, value)
	}
}

func TestWorkflowAIRunnerRetainsPartialUsageOnError(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	runner := WorkflowAIRunner{Runner: fixedWorkflowAgentRunner{
		err: wantErr,
		output: map[string]any{"usage": []workflows.AgentUsage{{
			Model: "private-model", ProviderCalls: 2, UsageReportedCalls: 1,
			PromptTokens: 10, CachedTokens: 4,
			CompletionTokens: 3, ReasoningTokens: 2, TotalTokens: 13,
			LatencyMillis: 50,
		}}, "usage_complete": false},
	}}
	result, err := runner.RunIsolated(t.Context(), IsolatedAIRequest{
		Operation: "scope.audit", SystemPrompt: "Audit privately.",
		UserPrompt: "Audit this diff.", Schema: map[string]any{"type": "object"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if result.Complete || result.Structured != nil || result.Usage.ProviderCalls != 2 ||
		result.Usage.UsageReportedCalls != 1 || result.Usage.PromptTokens != 10 ||
		result.Usage.CachedTokens != 4 || result.Usage.CompletionTokens != 3 ||
		result.Usage.ReasoningTokens != 2 || result.Usage.TotalTokens != 13 ||
		result.Usage.LatencyMillis != 50 {
		t.Fatalf("partial result = %#v", result)
	}
}

func TestWorkflowAIRunnerRejectsMalformedUsage(t *testing.T) {
	runner := WorkflowAIRunner{Runner: fixedWorkflowAgentRunner{output: map[string]any{
		"structured":     map[string]any{"ok": true},
		"usage_complete": false,
		"usage": []workflows.AgentUsage{
			{
				ProviderCalls: 1, UsageReportedCalls: 1,
				PromptTokens: 5, CachedTokens: 2,
				CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 7,
				LatencyMillis: 10,
			},
			{
				ProviderCalls: 1, UsageReportedCalls: 1,
				CompletionTokens: 1, ReasoningTokens: 2, TotalTokens: 1,
			},
		},
	}}}
	result, err := runner.RunIsolated(t.Context(), IsolatedAIRequest{
		Operation: "scope.audit", SystemPrompt: "Audit privately.",
		UserPrompt: "Audit this diff.", Schema: map[string]any{"type": "object"},
	})
	if err == nil || result.Complete || result.Structured != nil ||
		result.Usage.ProviderCalls != 1 || result.Usage.UsageReportedCalls != 1 ||
		result.Usage.PromptTokens != 5 || result.Usage.CachedTokens != 2 ||
		result.Usage.CompletionTokens != 2 || result.Usage.ReasoningTokens != 1 ||
		result.Usage.TotalTokens != 7 || result.Usage.LatencyMillis != 10 {
		t.Fatalf("malformed usage result = %#v, error = %v", result, err)
	}
}

func TestWorkflowAIRunnerRequiresAuthoritativeCompletenessSignal(t *testing.T) {
	runner := WorkflowAIRunner{Runner: fixedWorkflowAgentRunner{output: map[string]any{
		"structured": map[string]any{"ok": true},
		"usage": []workflows.AgentUsage{{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
		}},
	}}}
	result, err := runner.RunIsolated(t.Context(), IsolatedAIRequest{
		Operation: "scope.audit", SystemPrompt: "Audit privately.",
		UserPrompt: "Audit this diff.", Schema: map[string]any{"type": "object"},
	})
	if err == nil || result.Complete || result.Usage.ProviderCalls != 1 ||
		result.Usage.TotalTokens != 3 {
		t.Fatalf("missing completeness result = %#v, error = %v", result, err)
	}
}
