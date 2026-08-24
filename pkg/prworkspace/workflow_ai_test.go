package prworkspace

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

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
	source := aiExecutionSourceFromValue(value)
	if source == nil || source.AgentID != "main" || source.Session != aiExecutionSourceSessionKey(source) ||
		source.SessionRevision != "sha256:source-revision" {
		t.Fatalf("source = %#v value=%#v", source, value)
	}
}
