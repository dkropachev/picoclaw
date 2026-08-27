package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestP012PipelinePolicyHelperCoverage(t *testing.T) {
	pipeline := &Pipeline{}
	pipeline.emitToolPolicyDecision(nil, nil, tools.ToolFulfillmentExecute, ToolPolicyOutcomeAllow, "ignored")
	if outcome, reason := toolPolicyFailureOutcome(context.Canceled); outcome != ToolPolicyOutcomeCanceled ||
		reason != "policy_canceled" {
		t.Fatalf("canceled outcome = %q/%q", outcome, reason)
	}
	if outcome, reason := toolPolicyFailureOutcome(
		context.DeadlineExceeded,
	); outcome != ToolPolicyOutcomeCanceled ||
		reason != "policy_canceled" {
		t.Fatalf("deadline outcome = %q/%q", outcome, reason)
	}
	if outcome, reason := toolPolicyFailureOutcome(
		errors.New("failure"),
	); outcome != ToolPolicyOutcomeError ||
		reason != "policy_error" {
		t.Fatalf("error outcome = %q/%q", outcome, reason)
	}

	definitions := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "coverage_tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"text": map[string]any{"type": "string"}},
			},
		},
	}}
	if _, ok := exactOfferedToolDefinition(definitions, "missing"); ok {
		t.Fatal("missing definition resolved")
	}
	if invocation, err := prepareAgentToolInvocation(nil, "coverage_tool", nil); invocation != nil || err == nil {
		t.Fatalf("nil execution prepare = %#v/%v", invocation, err)
	}

	registry := tools.NewToolRegistry()
	registry.Register(&pipelineToolPolicyEffect{name: "coverage_tool", output: "ok"})
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}
	exec := &turnExecution{toolCatalog: catalog, providerToolDefs: definitions}
	if invocation, prepareErr := prepareAgentToolInvocation(
		exec,
		"missing",
		nil,
	); invocation != nil ||
		prepareErr == nil {
		t.Fatalf("unoffered prepare = %#v/%v", invocation, prepareErr)
	}
	if invocation, prepareErr := prepareAgentToolInvocation(
		exec,
		"coverage_tool",
		map[string]any{"text": 42},
	); invocation != nil || !errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("invalid schema prepare = %#v/%v", invocation, prepareErr)
	}
	invocation, err := prepareAgentToolInvocation(
		exec,
		"coverage_tool",
		map[string]any{"text": "ok"},
	)
	if err != nil {
		t.Fatalf("valid prepare error = %v", err)
	}
	if _, authorizationErr := pipeline.authorizeAgentTool(
		context.Background(),
		nil,
		providers.ToolCall{ID: "coverage-call"},
		invocation,
		tools.ToolFulfillmentExecute,
		tools.ToolHookProvenance{},
	); !errors.Is(authorizationErr, tools.ErrToolPolicyUnavailable) {
		t.Fatalf("nil turn authorization error = %v", authorizationErr)
	}
	turn := &turnState{
		agentID: "main", sessionKey: "session", turnID: "turn",
		toolPolicy: tools.CompatibilityAllowToolPolicy{},
	}
	authorization, err := pipeline.authorizeAgentTool(
		context.Background(),
		turn,
		providers.ToolCall{ID: "coverage-call"},
		invocation,
		tools.ToolFulfillmentExecute,
		tools.ToolHookProvenance{},
	)
	if err != nil || !authorization.allowed || authorization.reasonCode != "policy_allowed" {
		t.Fatalf("compat authorization = %#v/%v", authorization, err)
	}
}

func TestP012PipelineExecuteEarlyFailureCoverage(t *testing.T) {
	state := &turnState{hardAbort: true}
	control, err := (&Pipeline{}).ExecuteTools(
		context.Background(),
		context.Background(),
		state,
		&turnExecution{normalizedToolCalls: []providers.ToolCall{{ID: "hard", Name: "tool"}}},
		1,
	)
	if err != nil || control != ToolControlBreak {
		t.Fatalf("hard-abort ExecuteTools() = %v/%v", control, err)
	}

	state = &turnState{}
	control, err = (&Pipeline{}).ExecuteTools(
		context.Background(),
		context.Background(),
		state,
		&turnExecution{normalizedToolCalls: []providers.ToolCall{{
			ID: "invalid", Name: "tool", Arguments: map[string]any{"bad": make(chan int)},
		}}},
		1,
	)
	if err == nil || control != ToolControlBreak {
		t.Fatalf("invalid-argument ExecuteTools() = %v/%v", control, err)
	}
}
