package agent

import (
	"context"
	"errors"
	"fmt"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	toolPolicyDeniedMessage      = "Tool execution denied by policy."
	toolPolicyUnavailableMessage = "Tool authorization is temporarily unavailable."
	toolApprovalDeniedMessage    = "Tool execution denied by approval hook."
)

type agentToolAuthorization struct {
	allowed    bool
	reasonCode string
	message    string
}

func (p *Pipeline) emitToolPolicyDecision(
	ts *turnState,
	invocation *tools.PreparedToolInvocation,
	fulfillment tools.ToolFulfillmentKind,
	outcome ToolPolicyOutcome,
	reasonCode string,
) {
	if p == nil || p.al == nil || ts == nil || invocation == nil {
		return
	}
	p.al.emitEvent(
		runtimeevents.KindAgentToolPolicyDecision,
		ts.eventMeta("runTurn", "turn.tool.policy"),
		ToolPolicyDecisionPayload{
			Tool:        invocation.Name(),
			Risk:        invocation.Traits().Risk,
			Fulfillment: fulfillment,
			Outcome:     outcome,
			ReasonCode:  reasonCode,
		},
	)
}

func toolPolicyFailureOutcome(err error) (ToolPolicyOutcome, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ToolPolicyOutcomeCanceled, "policy_canceled"
	}
	return ToolPolicyOutcomeError, "policy_error"
}

func exactOfferedToolDefinition(
	definitions []providers.ToolDefinition,
	name string,
) (providers.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Function.Name == name {
			return definition, true
		}
	}
	return providers.ToolDefinition{}, false
}

func prepareAgentToolInvocation(
	exec *turnExecution,
	toolName string,
	toolArguments map[string]any,
) (*tools.PreparedToolInvocation, error) {
	if exec == nil || exec.toolCatalog == nil {
		return nil, fmt.Errorf("%w: turn tool catalog is unavailable", tools.ErrToolCatalogUnavailable)
	}
	offeredDefinition, offered := exactOfferedToolDefinition(exec.providerToolDefs, toolName)
	if !offered {
		return nil, fmt.Errorf("tool %q was not offered to the successful provider", toolName)
	}
	invocation, err := exec.toolCatalog.PrepareInvocation(toolName, toolArguments)
	if err != nil {
		return nil, err
	}
	if err := invocation.ValidateOfferedDefinition(offeredDefinition); err != nil {
		return nil, err
	}
	return invocation, nil
}

func (p *Pipeline) authorizeAgentTool(
	ctx context.Context,
	ts *turnState,
	call providers.ToolCall,
	invocation *tools.PreparedToolInvocation,
	fulfillment tools.ToolFulfillmentKind,
	hook tools.ToolHookProvenance,
) (agentToolAuthorization, error) {
	if ts == nil || invocation == nil {
		return agentToolAuthorization{}, fmt.Errorf(
			"%w: incomplete authorization request",
			tools.ErrToolPolicyUnavailable,
		)
	}
	arguments, err := invocation.PolicyArguments()
	if err != nil {
		return agentToolAuthorization{}, err
	}
	decision, err := tools.EvaluateToolPolicy(ctx, ts.toolPolicy, tools.ToolPolicyRequest{
		Subject: tools.ToolPolicySubject{
			AgentID:    ts.agentID,
			SessionKey: ts.sessionKey,
			TurnID:     ts.turnID,
			ToolCallID: call.ID,
			Source:     tools.ToolPolicySourceAgentPipeline,
		},
		Tool:        invocation.Name(),
		Arguments:   arguments,
		Traits:      invocation.Traits(),
		Fulfillment: fulfillment,
		Hook:        hook,
	})
	if err != nil {
		return agentToolAuthorization{}, err
	}
	if decision.Kind == tools.ToolPolicyDecisionDeny {
		return agentToolAuthorization{
			reasonCode: "policy_denied",
			message:    toolPolicyDeniedMessage,
		}, nil
	}

	if p.Hooks != nil {
		approval := p.Hooks.ApproveTool(ctx, &ToolApprovalRequest{
			Meta:      ts.eventMeta("runTurn", "turn.tool.approve"),
			Context:   cloneTurnContext(ts.turnCtx),
			Tool:      invocation.Name(),
			Arguments: arguments,
		})
		if !approval.Approved {
			return agentToolAuthorization{
				reasonCode: "approval_denied",
				message:    toolApprovalDeniedMessage,
			}, nil
		}
	}
	return agentToolAuthorization{allowed: true, reasonCode: "policy_allowed"}, nil
}
