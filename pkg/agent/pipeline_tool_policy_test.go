package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type pipelineToolPolicyProbe struct {
	decision   tools.ToolPolicyDecision
	err        error
	panicValue any
	onEvaluate func(context.Context, tools.ToolPolicyRequest)
	evaluate   func(int64, context.Context, tools.ToolPolicyRequest) (tools.ToolPolicyDecision, error)

	calls atomic.Int64
	mu    sync.Mutex
	reqs  []tools.ToolPolicyRequest
}

func (policy *pipelineToolPolicyProbe) EvaluateTool(
	ctx context.Context,
	request tools.ToolPolicyRequest,
) (tools.ToolPolicyDecision, error) {
	callNumber := policy.calls.Add(1)
	request.Arguments, _ = tools.DetachToolArguments(request.Arguments)
	policy.mu.Lock()
	policy.reqs = append(policy.reqs, request)
	policy.mu.Unlock()

	if policy.onEvaluate != nil {
		policy.onEvaluate(ctx, request)
	}
	if policy.panicValue != nil {
		panic(policy.panicValue)
	}
	if policy.evaluate != nil {
		return policy.evaluate(callNumber, ctx, request)
	}
	return policy.decision, policy.err
}

func (policy *pipelineToolPolicyProbe) requests() []tools.ToolPolicyRequest {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	requests := make([]tools.ToolPolicyRequest, len(policy.reqs))
	for index, request := range policy.reqs {
		requests[index] = request
		requests[index].Arguments, _ = tools.DetachToolArguments(request.Arguments)
	}
	return requests
}

type pipelineToolPolicyProvider struct {
	toolCalls []providers.ToolCall
	entered   chan struct{}
	release   <-chan struct{}
	enterOnce sync.Once

	mu          sync.Mutex
	calls       int
	messages    [][]providers.Message
	definitions [][]providers.ToolDefinition
}

func (provider *pipelineToolPolicyProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.calls++
	callNumber := provider.calls
	provider.messages = append(provider.messages, cloneProviderMessages(messages))
	provider.definitions = append(provider.definitions, cloneToolDefinitions(definitions))
	provider.mu.Unlock()

	if callNumber == 1 {
		if provider.entered != nil {
			provider.enterOnce.Do(func() { close(provider.entered) })
		}
		if provider.release != nil {
			select {
			case <-provider.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &providers.LLMResponse{
			ToolCalls:    cloneProviderToolCalls(provider.toolCalls),
			FinishReason: "tool_calls",
		}, nil
	}

	content := "done"
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "tool" {
			content = messages[index].Content
			break
		}
	}
	return &providers.LLMResponse{Content: content, FinishReason: "stop"}, nil
}

func (provider *pipelineToolPolicyProvider) snapshot() (int, [][]providers.Message, [][]providers.ToolDefinition) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	messages := make([][]providers.Message, len(provider.messages))
	for index := range provider.messages {
		messages[index] = cloneProviderMessages(provider.messages[index])
	}
	definitions := make([][]providers.ToolDefinition, len(provider.definitions))
	for index := range provider.definitions {
		definitions[index] = cloneToolDefinitions(provider.definitions[index])
	}
	return provider.calls, messages, definitions
}

type pipelineToolPolicyEffect struct {
	name   string
	output string
	calls  atomic.Int64

	mu   sync.Mutex
	args map[string]any
}

func (tool *pipelineToolPolicyEffect) Name() string { return tool.name }

func (tool *pipelineToolPolicyEffect) Description() string {
	return "P012 policy dispatch canary"
}

func (tool *pipelineToolPolicyEffect) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
		"required":             []any{"text"},
		"additionalProperties": false,
	}
}

func (tool *pipelineToolPolicyEffect) Execute(
	_ context.Context,
	arguments map[string]any,
) *tools.ToolResult {
	tool.calls.Add(1)
	detached, _ := tools.DetachToolArguments(arguments)
	tool.mu.Lock()
	tool.args = detached
	tool.mu.Unlock()
	return tools.SilentResult(tool.output)
}

func (tool *pipelineToolPolicyEffect) arguments() map[string]any {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	detached, _ := tools.DetachToolArguments(tool.args)
	return detached
}

type pipelineToolPolicyAfterHook struct {
	afterCalls atomic.Int64
}

func (hook *pipelineToolPolicyAfterHook) BeforeTool(
	_ context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	return request.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func (hook *pipelineToolPolicyAfterHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	hook.afterCalls.Add(1)
	return response.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type pipelineToolPolicyRespondHook struct {
	afterCalls atomic.Int64
}

func (hook *pipelineToolPolicyRespondHook) BeforeTool(
	_ context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	next := request.Clone()
	next.HookResult = &tools.ToolResult{
		ForLLM:          "private-hook-result",
		ForUser:         "private-hook-user-output",
		ResponseHandled: true,
	}
	return next, HookDecision{Action: HookActionRespond}, nil
}

func (hook *pipelineToolPolicyRespondHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	hook.afterCalls.Add(1)
	return response.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type pipelineToolPolicyApprovalHook struct {
	approved bool
	reason   string
	calls    atomic.Int64
}

func (hook *pipelineToolPolicyApprovalHook) ApproveTool(
	_ context.Context,
	_ *ToolApprovalRequest,
) (ApprovalDecision, error) {
	hook.calls.Add(1)
	return ApprovalDecision{Approved: hook.approved, Reason: hook.reason}, nil
}

type pipelineToolPolicyRewriteHook struct {
	target     string
	text       string
	afterCalls atomic.Int64
}

type pipelineToolPolicyAfterLLMRewriteHook struct {
	target string
	text   string
}

func (hook pipelineToolPolicyAfterLLMRewriteHook) BeforeLLM(
	_ context.Context,
	request *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	return request.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func (hook pipelineToolPolicyAfterLLMRewriteHook) AfterLLM(
	_ context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	next := response.Clone()
	if next != nil && next.Response != nil && len(next.Response.ToolCalls) > 0 {
		next.Response.ToolCalls[0].Name = hook.target
		next.Response.ToolCalls[0].Arguments = map[string]any{"text": hook.text}
	}
	return next, HookDecision{Action: HookActionModify}, nil
}

type pipelineToolPolicyInvalidNameHook struct {
	name string
}

func (hook pipelineToolPolicyInvalidNameHook) BeforeTool(
	_ context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	next := request.Clone()
	next.Tool = hook.name
	return next, HookDecision{Action: HookActionModify}, nil
}

func (hook pipelineToolPolicyInvalidNameHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return response.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func (hook *pipelineToolPolicyRewriteHook) BeforeTool(
	_ context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	next := request.Clone()
	next.Tool = hook.target
	next.Arguments["text"] = hook.text
	return next, HookDecision{Action: HookActionModify}, nil
}

func (hook *pipelineToolPolicyRewriteHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	hook.afterCalls.Add(1)
	return response.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func newPipelineToolPolicyLoop(
	t *testing.T,
	provider providers.LLMProvider,
	policy tools.ToolPolicy,
	feedback bool,
) (*AgentLoop, *AgentInstance, *bus.MessageBus) {
	t.Helper()
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         t.TempDir(),
		ModelName:         "test-model",
		MaxTokens:         4096,
		MaxToolIterations: 4,
		ToolFeedback:      config.ToolFeedbackConfig{Enabled: feedback},
	}}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		provider,
		WithToolPolicy(policy),
	)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	for _, name := range agent.Tools.List() {
		agent.Tools.Unregister(name)
	}
	return loop, agent, messageBus
}

func runPipelineToolPolicyTurn(
	ctx context.Context,
	loop *AgentLoop,
	agent *AgentInstance,
	sessionKey string,
) (string, error) {
	return loop.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run the policy canary",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true,
	})
}

func subscribePipelineToolPolicyEvents(
	t *testing.T,
	loop *AgentLoop,
	buffer int,
) (<-chan runtimeevents.Event, func()) {
	t.Helper()
	return subscribeRuntimeEventsForTest(
		t,
		loop,
		buffer,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecEnd,
		runtimeevents.KindAgentToolExecSkipped,
	)
}

func assertPipelineToolPolicyEventSequence(
	t *testing.T,
	events []runtimeevents.Event,
	want ...runtimeevents.Kind,
) {
	t.Helper()
	got := make([]runtimeevents.Kind, len(events))
	for index, event := range events {
		got[index] = event.Kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy event sequence = %v, want %v; events=%#v", got, want, events)
	}
}

func requirePipelineToolPolicyPayload(
	t *testing.T,
	events []runtimeevents.Event,
) ToolPolicyDecisionPayload {
	t.Helper()
	if len(events) == 0 || events[0].Kind != runtimeevents.KindAgentToolPolicyDecision {
		t.Fatalf("first event is not a policy decision: %#v", events)
	}
	payload, ok := events[0].Payload.(ToolPolicyDecisionPayload)
	if !ok {
		t.Fatalf("policy payload type = %T, want ToolPolicyDecisionPayload", events[0].Payload)
	}
	return payload
}

func drainPipelineToolPolicyOutbound(messageBus *bus.MessageBus) []bus.OutboundMessage {
	var messages []bus.OutboundMessage
	for {
		select {
		case message := <-messageBus.OutboundChan():
			messages = append(messages, message)
		default:
			return messages
		}
	}
}

func TestPipelineToolPolicyAllowDenyAndInfrastructureFailures(t *testing.T) {
	rawPolicyError := "raw-policy-error-must-not-leak"
	rawPolicyPanic := "raw-policy-panic-must-not-leak"
	tests := []struct {
		name            string
		policy          func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy)
		wantOutcome     ToolPolicyOutcome
		wantReason      string
		wantError       error
		wantEffect      int64
		wantAfter       int64
		wantFeedback    int
		wantProvider    int
		wantPolicyCalls int64
		wantResponse    string
	}{
		{
			name: "allow",
			policy: func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				probe := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
					Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "test_allow",
				}}
				return probe, probe
			},
			wantOutcome: ToolPolicyOutcomeAllow, wantReason: "policy_allowed",
			wantEffect: 1, wantAfter: 1, wantFeedback: 1, wantProvider: 2,
			wantPolicyCalls: 1, wantResponse: "tool-ok",
		},
		{
			name: "deny",
			policy: func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				probe := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
					Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "test_deny",
				}}
				return probe, probe
			},
			wantOutcome: ToolPolicyOutcomeDeny, wantReason: "policy_denied",
			wantFeedback: 0, wantProvider: 2, wantPolicyCalls: 1,
			wantResponse: toolPolicyDeniedMessage,
		},
		{
			name: "nil",
			policy: func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				return nil, nil
			},
			wantOutcome: ToolPolicyOutcomeError, wantReason: "policy_error",
			wantError: tools.ErrToolPolicyUnavailable, wantProvider: 1,
		},
		{
			name: "error",
			policy: func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				probe := &pipelineToolPolicyProbe{
					err: errors.New(rawPolicyError),
				}
				return probe, probe
			},
			wantOutcome: ToolPolicyOutcomeError, wantReason: "policy_error",
			wantError: tools.ErrToolPolicyUnavailable, wantProvider: 1, wantPolicyCalls: 1,
		},
		{
			name: "panic",
			policy: func(context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				probe := &pipelineToolPolicyProbe{panicValue: rawPolicyPanic}
				return probe, probe
			},
			wantOutcome: ToolPolicyOutcomeError, wantReason: "policy_error",
			wantError: tools.ErrToolPolicyUnavailable, wantProvider: 1, wantPolicyCalls: 1,
		},
		{
			name: "cancel",
			policy: func(cancel context.CancelFunc) (*pipelineToolPolicyProbe, tools.ToolPolicy) {
				probe := &pipelineToolPolicyProbe{
					decision: tools.ToolPolicyDecision{
						Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "unused_allow",
					},
					onEvaluate: func(context.Context, tools.ToolPolicyRequest) { cancel() },
				}
				return probe, probe
			},
			wantOutcome: ToolPolicyOutcomeCanceled, wantReason: "policy_canceled",
			wantError: context.Canceled, wantProvider: 1, wantPolicyCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			if test.name == "cancel" {
				ctx, cancel = context.WithCancel(context.Background())
				defer cancel()
			}
			probe, policy := test.policy(cancel)
			provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
				ID: "call-policy", Name: "policy_tool", Arguments: map[string]any{"text": "original"},
			}}}
			loop, agent, messageBus := newPipelineToolPolicyLoop(t, provider, policy, true)
			tool := &pipelineToolPolicyEffect{name: "policy_tool", output: "tool-ok"}
			agent.Tools.Register(tool)
			afterHook := &pipelineToolPolicyAfterHook{}
			if err := loop.MountHook(NamedHook("policy-after", afterHook)); err != nil {
				t.Fatalf("MountHook() error = %v", err)
			}
			eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
			defer closeEvents()

			response, runErr := runPipelineToolPolicyTurn(ctx, loop, agent, "policy-"+test.name)
			if test.wantError == nil {
				if runErr != nil {
					t.Fatalf("runAgentLoop() error = %v", runErr)
				}
			} else if !errors.Is(runErr, test.wantError) {
				t.Fatalf("runAgentLoop() error = %v, want %v", runErr, test.wantError)
			}
			if test.wantResponse != "" && response != test.wantResponse {
				t.Fatalf("response = %q, want %q", response, test.wantResponse)
			}
			for _, secret := range []string{rawPolicyError, rawPolicyPanic} {
				if strings.Contains(response, secret) || runErr != nil && strings.Contains(runErr.Error(), secret) {
					t.Fatalf(
						"raw policy detail %q leaked through response/error: response=%q error=%v",
						secret,
						response,
						runErr,
					)
				}
			}

			events := collectRuntimeEventStream(eventStream)
			if test.wantOutcome == ToolPolicyOutcomeAllow {
				assertPipelineToolPolicyEventSequence(
					t, events,
					runtimeevents.KindAgentToolPolicyDecision,
					runtimeevents.KindAgentToolExecStart,
					runtimeevents.KindAgentToolExecEnd,
				)
			} else {
				assertPipelineToolPolicyEventSequence(
					t, events,
					runtimeevents.KindAgentToolPolicyDecision,
					runtimeevents.KindAgentToolExecSkipped,
				)
			}
			payload := requirePipelineToolPolicyPayload(t, events)
			if payload.Tool != "policy_tool" || payload.Risk != tools.ToolRiskUnknown ||
				payload.Fulfillment != tools.ToolFulfillmentExecute ||
				payload.Outcome != test.wantOutcome || payload.ReasonCode != test.wantReason {
				t.Fatalf("policy decision payload = %#v", payload)
			}
			encodedEvents, encodeErr := json.Marshal(events)
			if encodeErr != nil {
				t.Fatalf("json.Marshal(events) error = %v", encodeErr)
			}
			if strings.Contains(string(encodedEvents), rawPolicyError) ||
				strings.Contains(string(encodedEvents), rawPolicyPanic) {
				t.Fatalf("raw policy detail leaked through events: %s", encodedEvents)
			}

			if got := tool.calls.Load(); got != test.wantEffect {
				t.Fatalf("tool calls = %d, want %d", got, test.wantEffect)
			}
			if got := afterHook.afterCalls.Load(); got != test.wantAfter {
				t.Fatalf("AfterTool calls = %d, want %d", got, test.wantAfter)
			}
			if outbound := drainPipelineToolPolicyOutbound(messageBus); len(outbound) != test.wantFeedback {
				t.Fatalf("outbound messages = %#v, want count %d", outbound, test.wantFeedback)
			}
			providerCalls, _, _ := provider.snapshot()
			if providerCalls != test.wantProvider {
				t.Fatalf("provider calls = %d, want %d", providerCalls, test.wantProvider)
			}
			if probe == nil {
				if test.wantPolicyCalls != 0 {
					t.Fatalf("nil policy has nonzero expected calls %d", test.wantPolicyCalls)
				}
				return
			}
			if got := probe.calls.Load(); got != test.wantPolicyCalls {
				t.Fatalf("policy calls = %d, want %d", got, test.wantPolicyCalls)
			}
			requests := probe.requests()
			if len(requests) != int(test.wantPolicyCalls) {
				t.Fatalf("policy requests = %#v, want %d", requests, test.wantPolicyCalls)
			}
			if len(requests) == 1 {
				request := requests[0]
				if request.Subject.AgentID != agent.ID || request.Subject.SessionKey != "policy-"+test.name ||
					request.Subject.TurnID == "" || request.Subject.ToolCallID != "call-policy" ||
					request.Subject.Source != tools.ToolPolicySourceAgentPipeline ||
					request.Tool != "policy_tool" || request.Fulfillment != tools.ToolFulfillmentExecute {
					t.Fatalf("policy request = %#v", request)
				}
			}
		})
	}
}

func TestPipelineToolPolicyHookRespondCrossesPolicyAndApproval(t *testing.T) {
	tests := []struct {
		name              string
		policyDecision    tools.ToolPolicyDecision
		approvalAllowed   bool
		wantApprovalCalls int64
		wantReason        string
		wantResponse      string
	}{
		{
			name: "central deny precedes approval",
			policyDecision: tools.ToolPolicyDecision{
				Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "respond_policy_deny",
			},
			wantReason: "policy_denied", wantResponse: toolPolicyDeniedMessage,
		},
		{
			name: "approval denies central allow",
			policyDecision: tools.ToolPolicyDecision{
				Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "respond_policy_allow",
			},
			wantApprovalCalls: 1, wantReason: "approval_denied",
			wantResponse: toolApprovalDeniedMessage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &pipelineToolPolicyProbe{decision: test.policyDecision}
			provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
				ID: "call-respond", Name: "respond_tool", Arguments: map[string]any{"text": "original"},
			}}}
			loop, agent, messageBus := newPipelineToolPolicyLoop(t, provider, policy, true)
			effect := &pipelineToolPolicyEffect{name: "respond_tool", output: "registry-effect"}
			agent.Tools.Register(effect)
			respond := &pipelineToolPolicyRespondHook{}
			approval := &pipelineToolPolicyApprovalHook{
				approved: test.approvalAllowed,
				reason:   "approval-secret-" + strings.Repeat("x", 10_000),
			}
			if err := loop.MountHook(NamedHook("policy-respond", respond)); err != nil {
				t.Fatalf("MountHook(respond) error = %v", err)
			}
			if err := loop.MountHook(NamedHook("policy-approval", approval)); err != nil {
				t.Fatalf("MountHook(approval) error = %v", err)
			}
			eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
			defer closeEvents()

			response, runErr := runPipelineToolPolicyTurn(
				context.Background(), loop, agent, "policy-respond-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if runErr != nil {
				t.Fatalf("runAgentLoop() error = %v", runErr)
			}
			if response != test.wantResponse {
				t.Fatalf("response = %q, want %q", response, test.wantResponse)
			}
			if strings.Contains(response, "private-hook") || strings.Contains(response, "approval-secret") {
				t.Fatalf("denied hook response leaked: %q", response)
			}
			events := collectRuntimeEventStream(eventStream)
			assertPipelineToolPolicyEventSequence(
				t, events,
				runtimeevents.KindAgentToolPolicyDecision,
				runtimeevents.KindAgentToolExecSkipped,
			)
			payload := requirePipelineToolPolicyPayload(t, events)
			if payload.Tool != "respond_tool" || payload.Fulfillment != tools.ToolFulfillmentHookRespond ||
				payload.Outcome != ToolPolicyOutcomeDeny || payload.ReasonCode != test.wantReason {
				t.Fatalf("respond policy payload = %#v", payload)
			}
			encodedEvent, encodeErr := json.Marshal(events[0])
			if encodeErr != nil {
				t.Fatalf("json.Marshal(event) error = %v", encodeErr)
			}
			if strings.Contains(string(encodedEvent), approval.reason) ||
				strings.Contains(string(encodedEvent), "private-hook") {
				t.Fatalf("policy event leaked hook/approval text: %s", encodedEvent)
			}
			if effect.calls.Load() != 0 || respond.afterCalls.Load() != 0 {
				t.Fatalf("denied respond produced effect/AfterTool: effect=%d after=%d",
					effect.calls.Load(), respond.afterCalls.Load())
			}
			if got := approval.calls.Load(); got != test.wantApprovalCalls {
				t.Fatalf("approval calls = %d, want %d", got, test.wantApprovalCalls)
			}
			if outbound := drainPipelineToolPolicyOutbound(messageBus); len(outbound) != 0 {
				t.Fatalf("denied respond produced outbound output: %#v", outbound)
			}
			requests := policy.requests()
			if len(requests) != 1 || requests[0].Fulfillment != tools.ToolFulfillmentHookRespond ||
				requests[0].Hook.Name != "policy-respond" ||
				requests[0].Hook.Source != HookSourceInProcess.String() || !requests[0].Hook.Trusted {
				t.Fatalf("respond policy requests = %#v", requests)
			}
		})
	}
}

func TestPipelineToolPolicyAllowedHookRespondEmitsVirtualExecutionOnly(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "respond_allow",
	}}
	provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-respond-allow", Name: "respond_tool", Arguments: map[string]any{"text": "original"},
	}}}
	loop, agent, messageBus := newPipelineToolPolicyLoop(t, provider, policy, true)
	effect := &pipelineToolPolicyEffect{name: "respond_tool", output: "registry-effect"}
	agent.Tools.Register(effect)
	respond := &pipelineToolPolicyRespondHook{}
	approval := &pipelineToolPolicyApprovalHook{approved: true}
	if err := loop.MountHook(NamedHook("policy-respond-allow", respond)); err != nil {
		t.Fatalf("MountHook(respond) error = %v", err)
	}
	if err := loop.MountHook(NamedHook("policy-approval-allow", approval)); err != nil {
		t.Fatalf("MountHook(approval) error = %v", err)
	}
	eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
	defer closeEvents()

	response, runErr := runPipelineToolPolicyTurn(
		context.Background(), loop, agent, "policy-respond-allowed",
	)
	if runErr != nil {
		t.Fatalf("runAgentLoop() error = %v", runErr)
	}
	if response != "" {
		t.Fatalf("handled synthetic response = %q, want empty final response", response)
	}
	events := collectRuntimeEventStream(eventStream)
	assertPipelineToolPolicyEventSequence(
		t, events,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecEnd,
	)
	payload := requirePipelineToolPolicyPayload(t, events)
	if payload.Tool != "respond_tool" || payload.Fulfillment != tools.ToolFulfillmentHookRespond ||
		payload.Outcome != ToolPolicyOutcomeAllow || payload.ReasonCode != "policy_allowed" {
		t.Fatalf("allowed respond policy payload = %#v", payload)
	}
	if effect.calls.Load() != 0 || respond.afterCalls.Load() != 0 || approval.calls.Load() != 1 {
		t.Fatalf("allowed respond registry/AfterTool/approval = %d/%d/%d",
			effect.calls.Load(), respond.afterCalls.Load(), approval.calls.Load())
	}
	outbound := drainPipelineToolPolicyOutbound(messageBus)
	if len(outbound) != 2 {
		t.Fatalf("allowed respond outbound = %#v, want feedback plus hook user result", outbound)
	}
	joined := outbound[0].Content + "\n" + outbound[1].Content
	if !strings.Contains(joined, "respond_tool") || !strings.Contains(joined, "private-hook-user-output") {
		t.Fatalf("allowed respond outbound content = %q", joined)
	}
}

func TestPipelineToolPolicyDenyPreventsTrackedSpawnAdmission(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "deny_spawn",
	}}
	provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-spawn-denied", Name: "spawn", Arguments: map[string]any{"task": "must not start"},
	}}}
	loop, agent, _ := newPipelineToolPolicyLoop(t, provider, policy, false)
	manager := tools.NewSubagentManager(provider, "test-model", t.TempDir())
	agent.Tools.Register(tools.NewSpawnTool(manager))
	eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
	defer closeEvents()

	response, runErr := runPipelineToolPolicyTurn(
		context.Background(), loop, agent, "policy-denied-spawn",
	)
	if runErr != nil || response != toolPolicyDeniedMessage {
		t.Fatalf("denied spawn response/error = %q / %v", response, runErr)
	}
	if tasks := manager.ListTaskCopies(); len(tasks) != 0 {
		t.Fatalf("policy-denied spawn admitted tasks: %#v", tasks)
	}
	events := collectRuntimeEventStream(eventStream)
	assertPipelineToolPolicyEventSequence(
		t, events,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecSkipped,
	)
	if payload := requirePipelineToolPolicyPayload(t, events); payload.Tool != "spawn" ||
		payload.Outcome != ToolPolicyOutcomeDeny || payload.ReasonCode != "policy_denied" {
		t.Fatalf("denied spawn policy payload = %#v", payload)
	}
}

func TestPipelineToolPolicyUsesFinalToolAndFrozenTraits(t *testing.T) {
	tests := []struct {
		name         string
		providerTool string
		finalTool    string
		rewrite      bool
		wantRisk     tools.ToolRiskClass
		wantText     string
		wantHookName string
	}{
		{
			name:         "legacy unknown",
			providerTool: "legacy_unknown", finalTool: "legacy_unknown",
			wantRisk: tools.ToolRiskUnknown, wantText: "original",
		},
		{
			name:         "trusted rewrite to destructive",
			providerTool: "rewrite_source", finalTool: "destructive_final", rewrite: true,
			wantRisk: tools.ToolRiskDestructive, wantText: "rewritten",
			wantHookName: "policy-rewrite",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
				Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "traits_allow",
			}}
			provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
				ID: "call-traits", Name: test.providerTool, Arguments: map[string]any{"text": "original"},
			}}}
			loop, agent, _ := newPipelineToolPolicyLoop(t, provider, policy, false)
			original := &pipelineToolPolicyEffect{name: test.providerTool, output: "original-effect"}
			agent.Tools.Register(original)
			final := original
			var rewrite *pipelineToolPolicyRewriteHook
			if test.rewrite {
				final = &pipelineToolPolicyEffect{name: test.finalTool, output: "final-effect"}
				factory, factoryErr := tools.NewToolFactoryFromPrototype(
					final,
					tools.ToolTraits{Risk: tools.ToolRiskDestructive},
					func(tools.ToolBuildContext) (tools.Tool, error) {
						return &pipelineToolPolicyEffect{name: test.finalTool, output: "owner-final-effect"}, nil
					},
				)
				if factoryErr != nil {
					t.Fatalf("NewToolFactoryFromPrototype() error = %v", factoryErr)
				}
				if registerErr := agent.Tools.RegisterFactoryBacked(final, factory); registerErr != nil {
					t.Fatalf("RegisterFactoryBacked() error = %v", registerErr)
				}
				rewrite = &pipelineToolPolicyRewriteHook{target: test.finalTool, text: test.wantText}
				if err := loop.MountHook(NamedHook("policy-rewrite", rewrite)); err != nil {
					t.Fatalf("MountHook(rewrite) error = %v", err)
				}
			}
			eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
			defer closeEvents()

			response, runErr := runPipelineToolPolicyTurn(
				context.Background(), loop, agent, "policy-traits-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if runErr != nil {
				t.Fatalf("runAgentLoop() error = %v", runErr)
			}
			if test.rewrite {
				if response != "final-effect" || original.calls.Load() != 0 || final.calls.Load() != 1 {
					t.Fatalf("rewrite dispatch: response=%q original=%d final=%d",
						response, original.calls.Load(), final.calls.Load())
				}
			} else if response != "original-effect" || original.calls.Load() != 1 {
				t.Fatalf("legacy dispatch: response=%q calls=%d", response, original.calls.Load())
			}
			if got := final.arguments(); !reflect.DeepEqual(got, map[string]any{"text": test.wantText}) {
				t.Fatalf("final execution arguments = %#v", got)
			}
			requests := policy.requests()
			if len(requests) != 1 {
				t.Fatalf("policy requests = %#v", requests)
			}
			request := requests[0]
			if request.Tool != test.finalTool || request.Traits.Risk != test.wantRisk ||
				request.Arguments["text"] != test.wantText || request.Hook.Name != test.wantHookName {
				t.Fatalf("final policy request = %#v", request)
			}
			if test.rewrite && (!request.Hook.Trusted || request.Hook.Source != HookSourceInProcess.String()) {
				t.Fatalf("rewrite provenance = %#v", request.Hook)
			}
			events := collectRuntimeEventStream(eventStream)
			assertPipelineToolPolicyEventSequence(
				t, events,
				runtimeevents.KindAgentToolPolicyDecision,
				runtimeevents.KindAgentToolExecStart,
				runtimeevents.KindAgentToolExecEnd,
			)
			payload := requirePipelineToolPolicyPayload(t, events)
			if payload.Tool != test.finalTool || payload.Risk != test.wantRisk ||
				payload.Fulfillment != tools.ToolFulfillmentExecute ||
				payload.Outcome != ToolPolicyOutcomeAllow {
				t.Fatalf("final policy event = %#v", payload)
			}
			if rewrite != nil && rewrite.afterCalls.Load() != 1 {
				t.Fatalf("rewrite AfterTool calls = %d, want 1", rewrite.afterCalls.Load())
			}
			_, _, definitions := provider.snapshot()
			if len(definitions) < 1 || !toolDefinitionsContainExactName(definitions[0], test.finalTool) {
				t.Fatalf("successful provider was not offered final tool %q: %#v", test.finalTool, definitions)
			}
		})
	}
}

func TestPipelineToolPolicyAttributesTrustedAfterLLMRewrite(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "deny_after_llm_rewrite",
	}}
	provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-after-llm-rewrite", Name: "original_tool",
		Arguments: map[string]any{"text": "original"},
	}}}
	loop, agent, _ := newPipelineToolPolicyLoop(t, provider, policy, false)
	agent.Tools.Register(&pipelineToolPolicyEffect{name: "original_tool", output: "original-effect"})
	agent.Tools.Register(&pipelineToolPolicyEffect{name: "rewritten_tool", output: "rewritten-effect"})
	hook := pipelineToolPolicyAfterLLMRewriteHook{
		target: "rewritten_tool", text: "trusted-after-llm",
	}
	if err := loop.MountHook(NamedHook("after-llm-authority", hook)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}

	response, runErr := runPipelineToolPolicyTurn(
		context.Background(), loop, agent, "policy-after-llm-provenance",
	)
	if runErr != nil || response != toolPolicyDeniedMessage {
		t.Fatalf("rewritten policy response/error = %q / %v", response, runErr)
	}
	requests := policy.requests()
	if len(requests) != 1 || requests[0].Tool != "rewritten_tool" ||
		requests[0].Arguments["text"] != "trusted-after-llm" ||
		requests[0].Hook.Name != "after-llm-authority" ||
		requests[0].Hook.Source != HookSourceInProcess.String() || !requests[0].Hook.Trusted {
		t.Fatalf("AfterLLM rewritten policy request = %#v", requests)
	}
}

func TestPipelineToolPolicyRejectsInvalidHookToolNameSafely(t *testing.T) {
	invalidName := "invalid\n" + strings.Repeat("x", tools.MaxToolPolicyNameLen+64)
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "must_not_evaluate",
	}}
	provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-invalid-hook-name", Name: "original_tool",
		Arguments: map[string]any{"text": "original"},
	}}}
	loop, agent, _ := newPipelineToolPolicyLoop(t, provider, policy, false)
	effect := &pipelineToolPolicyEffect{name: "original_tool", output: "must-not-execute"}
	agent.Tools.Register(effect)
	if err := loop.MountHook(NamedHook(
		"invalid-name-authority",
		pipelineToolPolicyInvalidNameHook{name: invalidName},
	)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
	defer closeEvents()

	response, runErr := runPipelineToolPolicyTurn(
		context.Background(), loop, agent, "policy-invalid-hook-name",
	)
	if runErr != nil || response != "Tool hook returned an invalid tool name." {
		t.Fatalf("invalid-name response/error = %q / %v", response, runErr)
	}
	events := collectRuntimeEventStream(eventStream)
	assertPipelineToolPolicyEventSequence(t, events, runtimeevents.KindAgentToolExecSkipped)
	payload, ok := events[0].Payload.(ToolExecSkippedPayload)
	if !ok || payload.Tool != "original_tool" || strings.Contains(payload.Reason, invalidName) {
		t.Fatalf("invalid-name skipped payload = %#v", events[0].Payload)
	}
	encoded, encodeErr := json.Marshal(events)
	if encodeErr != nil {
		t.Fatalf("json.Marshal(events) error = %v", encodeErr)
	}
	if strings.Contains(string(encoded), invalidName) || policy.calls.Load() != 0 || effect.calls.Load() != 0 {
		t.Fatalf("invalid hook name escaped or produced authority/effect: events=%s policy=%d effect=%d",
			encoded, policy.calls.Load(), effect.calls.Load())
	}
}

func toolDefinitionsContainExactName(definitions []providers.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Function.Name == name {
			return true
		}
	}
	return false
}

func TestPipelineToolPolicyInfrastructureFailureClosesOutWholeBatch(t *testing.T) {
	rawError := "raw-batch-policy-error-must-not-leak"
	policy := &pipelineToolPolicyProbe{evaluate: func(
		callNumber int64,
		_ context.Context,
		_ tools.ToolPolicyRequest,
	) (tools.ToolPolicyDecision, error) {
		if callNumber == 1 {
			return tools.ToolPolicyDecision{
				Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "first_call_allow",
			}, nil
		}
		return tools.ToolPolicyDecision{}, errors.New(rawError)
	}}
	provider := &pipelineToolPolicyProvider{}
	loop, agent, messageBus := newPipelineToolPolicyLoop(t, provider, policy, true)
	tool := &pipelineToolPolicyEffect{name: "batch_tool", output: "batch-effect"}
	agent.Tools.Register(tool)
	afterHook := &pipelineToolPolicyAfterHook{}
	if err := loop.MountHook(NamedHook("batch-after", afterHook)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
	defer closeEvents()

	calls := []providers.ToolCall{
		{ID: "call-a", Name: "batch_tool", Arguments: map[string]any{"text": "a"}},
		{ID: "call-b", Name: "batch_tool", Arguments: map[string]any{"text": "b"}},
		{ID: "call-c", Name: "batch_tool", Arguments: map[string]any{"text": "c"}},
	}
	catalog, catalogErr := agent.Tools.SnapshotModelToolCatalog()
	if catalogErr != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", catalogErr)
	}
	inbound := &bus.InboundContext{Channel: "cli", ChatID: "direct", ChatType: "direct"}
	opts := normalizeProcessOptions(processOptions{
		Dispatch: DispatchRequest{
			SessionKey:     "policy-batch",
			InboundContext: inbound,
			UserMessage:    "run batch",
		},
		NoHistory:       true,
		EnableSummary:   false,
		SendResponse:    false,
		DefaultResponse: defaultResponse,
	})
	scope := loop.newTurnEventScope(
		agent.ID,
		opts.Dispatch.SessionKey,
		newTurnContext(inbound, nil, nil),
	)
	state := newTurnState(agent, opts, scope)
	loop.prepareTurnState(state)
	execution := &turnExecution{
		messages:            nil,
		normalizedToolCalls: cloneProviderToolCalls(calls),
		providerToolDefs:    catalog.ProviderDefinitions(),
		toolCatalog:         catalog,
		response:            &providers.LLMResponse{ToolCalls: cloneProviderToolCalls(calls)},
		allResponsesHandled: true,
	}

	control, executeErr := NewPipeline(loop).ExecuteTools(
		context.Background(),
		context.Background(),
		state,
		execution,
		1,
	)
	if control != ToolControlBreak || !errors.Is(executeErr, tools.ErrToolPolicyUnavailable) {
		t.Fatalf("ExecuteTools() = (%v, %v), want break/ErrToolPolicyUnavailable", control, executeErr)
	}
	if strings.Contains(executeErr.Error(), rawError) {
		t.Fatalf("raw policy error leaked through ExecuteTools error: %v", executeErr)
	}

	wantIDs := []string{"call-a", "call-b", "call-c"}
	if len(execution.messages) != len(wantIDs) {
		t.Fatalf("closeout messages = %#v, want %d", execution.messages, len(wantIDs))
	}
	for index, message := range execution.messages {
		wantContent := toolPolicyUnavailableMessage
		if index == 0 {
			wantContent = "batch-effect"
		}
		if message.Role != "tool" || message.ToolCallID != wantIDs[index] ||
			message.Content != wantContent {
			t.Fatalf("closeout message %d = %#v", index, message)
		}
		if len(message.Content) > 128 || strings.Contains(message.Content, rawError) {
			t.Fatalf("closeout message is unbounded/private: %#v", message)
		}
	}

	events := collectRuntimeEventStream(eventStream)
	assertPipelineToolPolicyEventSequence(
		t, events,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecEnd,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecSkipped,
		runtimeevents.KindAgentToolExecSkipped,
	)
	firstPayload := requirePipelineToolPolicyPayload(t, events)
	secondPayload, ok := events[3].Payload.(ToolPolicyDecisionPayload)
	if firstPayload.Outcome != ToolPolicyOutcomeAllow || firstPayload.ReasonCode != "policy_allowed" ||
		!ok || secondPayload.Outcome != ToolPolicyOutcomeError || secondPayload.ReasonCode != "policy_error" {
		t.Fatalf("batch policy payloads = %#v / %#v", firstPayload, events[3].Payload)
	}
	encoded, encodeErr := json.Marshal(struct {
		Events   []runtimeevents.Event `json:"events"`
		Messages []providers.Message   `json:"messages"`
	}{Events: events, Messages: execution.messages})
	if encodeErr != nil {
		t.Fatalf("json.Marshal(closeout) error = %v", encodeErr)
	}
	if strings.Contains(string(encoded), rawError) {
		t.Fatalf("raw policy error leaked through closeout: %s", encoded)
	}
	if tool.calls.Load() != 1 || afterHook.afterCalls.Load() != 1 {
		t.Fatalf("later infrastructure failure lost/added earlier effect: effect=%d after=%d",
			tool.calls.Load(), afterHook.afterCalls.Load())
	}
	if policy.calls.Load() != 2 {
		t.Fatalf("policy calls = %d, want 2", policy.calls.Load())
	}
	if outbound := drainPipelineToolPolicyOutbound(messageBus); len(outbound) != 1 {
		t.Fatalf("later failure feedback = %#v, want only the earlier allowed call", outbound)
	}
}

func TestPipelineToolPolicyRootTurnSnapshotsPolicyAtAdmission(t *testing.T) {
	allowPolicy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "snapshot_allow",
	}}
	denyPolicy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "late_deny",
	}}
	entered := make(chan struct{})
	release := make(chan struct{})
	provider := &pipelineToolPolicyProvider{
		toolCalls: []providers.ToolCall{{
			ID: "call-snapshot", Name: "snapshot_tool", Arguments: map[string]any{"text": "snapshot"},
		}},
		entered: entered,
		release: release,
	}
	loop, agent, _ := newPipelineToolPolicyLoop(t, provider, allowPolicy, false)
	tool := &pipelineToolPolicyEffect{name: "snapshot_tool", output: "snapshot-effect"}
	agent.Tools.Register(tool)
	eventStream, closeEvents := subscribePipelineToolPolicyEvents(t, loop, 16)
	defer closeEvents()

	type turnResult struct {
		response string
		err      error
	}
	resultCh := make(chan turnResult, 1)
	go func() {
		response, err := runPipelineToolPolicyTurn(
			context.Background(), loop, agent, "policy-snapshot",
		)
		resultCh <- turnResult{response: response, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not enter after root policy snapshot")
	}
	// The provider call occurs after prepareTurnState has bound the turn's
	// immutable policy reference. A later loop-level replacement applies only to
	// subsequently admitted turns.
	loop.toolPolicy = denyPolicy
	close(release)

	var result turnResult
	select {
	case result = <-resultCh:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshotted-policy turn did not finish")
	}
	if result.err != nil || result.response != "snapshot-effect" {
		t.Fatalf("snapshotted-policy turn = (%q, %v)", result.response, result.err)
	}
	if allowPolicy.calls.Load() != 1 || denyPolicy.calls.Load() != 0 || tool.calls.Load() != 1 {
		t.Fatalf("policy snapshot calls: admitted=%d late=%d effect=%d",
			allowPolicy.calls.Load(), denyPolicy.calls.Load(), tool.calls.Load())
	}
	events := collectRuntimeEventStream(eventStream)
	assertPipelineToolPolicyEventSequence(
		t, events,
		runtimeevents.KindAgentToolPolicyDecision,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecEnd,
	)
	payload := requirePipelineToolPolicyPayload(t, events)
	if payload.Outcome != ToolPolicyOutcomeAllow || payload.ReasonCode != "policy_allowed" {
		t.Fatalf("snapshot policy payload = %#v", payload)
	}
}

func TestPipelineToolPolicyStrictChildInheritsBoundParentPolicy(t *testing.T) {
	parentPolicy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "parent_deny",
	}}
	lateLoopPolicy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "late_allow",
	}}
	loop := &AgentLoop{toolPolicy: lateLoopPolicy}
	parent := &turnState{toolPolicy: parentPolicy, toolPolicyBound: true}
	child := &turnState{parentTurnState: parent}

	loop.prepareTurnState(child)
	if !child.toolPolicyBound || child.toolPolicy != parentPolicy {
		t.Fatalf(
			"child policy = %T/%v, want exact bound parent %p",
			child.toolPolicy,
			child.toolPolicyBound,
			parentPolicy,
		)
	}
	loop.toolPolicy = &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind: tools.ToolPolicyDecisionAllow, ReasonCode: "newer_allow",
	}}
	loop.prepareTurnState(child)
	if child.toolPolicy != parentPolicy {
		t.Fatalf("reprepared child policy = %T, want exact parent snapshot", child.toolPolicy)
	}
}
