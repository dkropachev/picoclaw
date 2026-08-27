package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type p012CoverageLLMHook struct {
	before func(context.Context, *LLMHookRequest) (*LLMHookRequest, HookDecision, error)
	after  func(context.Context, *LLMHookResponse) (*LLMHookResponse, HookDecision, error)
}

func (hook p012CoverageLLMHook) BeforeLLM(
	ctx context.Context,
	request *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	if hook.before == nil {
		return request, HookDecision{Action: HookActionContinue}, nil
	}
	return hook.before(ctx, request)
}

func (hook p012CoverageLLMHook) AfterLLM(
	ctx context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	if hook.after == nil {
		return response, HookDecision{Action: HookActionContinue}, nil
	}
	return hook.after(ctx, response)
}

type p012CoverageToolHook struct {
	before func(context.Context, *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision, error)
	after  func(context.Context, *ToolResultHookResponse) (*ToolResultHookResponse, HookDecision, error)
}

func (hook p012CoverageToolHook) BeforeTool(
	ctx context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if hook.before == nil {
		return request, HookDecision{Action: HookActionContinue}, nil
	}
	return hook.before(ctx, request)
}

func (hook p012CoverageToolHook) AfterTool(
	ctx context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	if hook.after == nil {
		return response, HookDecision{Action: HookActionContinue}, nil
	}
	return hook.after(ctx, response)
}

type p012CoverageApprover struct {
	approve func(context.Context, *ToolApprovalRequest) (ApprovalDecision, error)
}

func (approver p012CoverageApprover) ApproveTool(
	ctx context.Context,
	request *ToolApprovalRequest,
) (ApprovalDecision, error) {
	return approver.approve(ctx, request)
}

type p012CoverageObserver func(context.Context, runtimeevents.Event) error

func (observer p012CoverageObserver) OnRuntimeEvent(
	ctx context.Context,
	event runtimeevents.Event,
) error {
	return observer(ctx, event)
}

type p012CoverageCloser struct {
	calls int
	err   error
}

func (closer *p012CoverageCloser) Close() error {
	closer.calls++
	return closer.err
}

type p012CoverageSubscription struct {
	runtimeevents.Subscription
	err error
}

func (subscription *p012CoverageSubscription) Close() error {
	return subscription.err
}

type p012CoverageEventChannel struct {
	runtimeevents.EventChannel
	subscription runtimeevents.Subscription
	events       <-chan runtimeevents.Event
	err          error
}

func (channel p012CoverageEventChannel) SubscribeChan(
	context.Context,
	runtimeevents.SubscribeOptions,
) (runtimeevents.Subscription, <-chan runtimeevents.Event, error) {
	return channel.subscription, channel.events, channel.err
}

type p012CoveragePolicyFunc func(
	context.Context,
	tools.ToolPolicyRequest,
) (tools.ToolPolicyDecision, error)

func (policy p012CoveragePolicyFunc) EvaluateTool(
	ctx context.Context,
	request tools.ToolPolicyRequest,
) (tools.ToolPolicyDecision, error) {
	return policy(ctx, request)
}

type p012CoverageTool struct{}

func (p012CoverageTool) Name() string        { return "p012_coverage_tool" }
func (p012CoverageTool) Description() string { return "coverage fixture" }
func (p012CoverageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func (p012CoverageTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.NewToolResult("unused")
}

func p012CoverageCycle() map[string]any {
	cycle := map[string]any{}
	cycle["self"] = cycle
	return cycle
}

func p012CoverageDefinition(name string, parameters map[string]any) providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:       name,
			Parameters: parameters,
		},
	}
}

func p012CoverageMount(t *testing.T, manager *HookManager, registration HookRegistration) {
	t.Helper()
	if err := manager.Mount(registration); err != nil {
		t.Fatalf("Mount(%q): %v", registration.Name, err)
	}
}

func p012CoverageLLMRequest() *LLMHookRequest {
	return &LLMHookRequest{
		Model: "model-before",
		Messages: []providers.Message{
			{Role: "system", Content: "fixed system"},
			{Role: "user", Content: "question"},
		},
		Tools: []providers.ToolDefinition{
			p012CoverageDefinition("offered", map[string]any{"type": "object"}),
		},
		Options: map[string]any{"temperature": 0},
	}
}

func p012CoverageLLMResponse() *LLMHookResponse {
	return &LLMHookResponse{
		Model: "model-after",
		Response: &providers.LLMResponse{
			Content: "original",
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "offered", Arguments: map[string]any{"value": "one"}},
			},
		},
	}
}

func TestP012HookCoverageCloneAndDetachDefenses(t *testing.T) {
	if (HookDecision{}).normalizedAction() != HookActionContinue {
		t.Fatal("zero hook action did not normalize to continue")
	}
	if got := (HookSource(99)).String(); got != "unknown" {
		t.Fatalf("unknown hook source = %q", got)
	}

	var llmRequest *LLMHookRequest
	var llmResponse *LLMHookResponse
	var toolRequest *ToolCallHookRequest
	var approvalRequest *ToolApprovalRequest
	var toolResponse *ToolResultHookResponse
	if llmRequest.Clone() != nil || llmResponse.Clone() != nil || toolRequest.Clone() != nil ||
		approvalRequest.Clone() != nil || toolResponse.Clone() != nil {
		t.Fatal("nil hook clone returned a non-nil value")
	}
	if cloneLLMResponse(nil) != nil || cloneToolResult(nil) != nil {
		t.Fatal("nil nested clone returned a non-nil value")
	}

	createdAt := time.Unix(123, 456)
	message := providers.Message{
		Role:        "assistant",
		CreatedAt:   &createdAt,
		Media:       []string{"media"},
		Attachments: []providers.Attachment{{Ref: "attachment"}},
		Parts:       []providers.PromptPart{{Text: "part"}},
		SystemParts: []providers.ContentBlock{{
			Text:         "system part",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:        "clone-call",
			Function:  &providers.FunctionCall{Name: "clone"},
			Arguments: map[string]any{"nested": map[string]any{"value": "before"}},
			ExtraContent: &providers.ExtraContent{
				Google: &providers.GoogleExtra{ThoughtSignature: "signature"},
			},
		}},
	}
	clonedMessages := cloneProviderMessages([]providers.Message{message})
	clonedMessages[0].Media[0] = "changed"
	clonedMessages[0].Attachments[0].Ref = "changed"
	clonedMessages[0].Parts[0].Text = "changed"
	clonedMessages[0].SystemParts[0].CacheControl.Type = "changed"
	clonedMessages[0].ToolCalls[0].Function.Name = "changed"
	clonedMessages[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature = "changed"
	clonedMessages[0].ToolCalls[0].Arguments["nested"].(map[string]any)["value"] = "changed"
	if message.Media[0] != "media" || message.Attachments[0].Ref != "attachment" ||
		message.Parts[0].Text != "part" || message.SystemParts[0].CacheControl.Type != "ephemeral" ||
		message.ToolCalls[0].Function.Name != "clone" ||
		message.ToolCalls[0].ExtraContent.Google.ThoughtSignature != "signature" ||
		message.ToolCalls[0].Arguments["nested"].(map[string]any)["value"] != "before" ||
		clonedMessages[0].CreatedAt == message.CreatedAt {
		t.Fatal("provider message clone retained mutable aliases")
	}

	response := &providers.LLMResponse{
		ToolCalls:        message.ToolCalls,
		ReasoningDetails: []protocoltypes.ReasoningDetail{{Text: "reason"}},
		Usage:            &providers.UsageInfo{TotalTokens: 3},
	}
	clonedResponse := cloneLLMResponse(response)
	clonedResponse.ReasoningDetails[0].Text = "changed"
	clonedResponse.Usage.TotalTokens = 9
	if response.ReasoningDetails[0].Text != "reason" || response.Usage.TotalTokens != 3 {
		t.Fatal("LLM response clone retained mutable aliases")
	}

	result := &tools.ToolResult{
		Media:        []string{"media"},
		ArtifactTags: []string{"artifact"},
		Messages:     []providers.Message{message},
	}
	clonedResult := cloneToolResult(result)
	clonedResult.Media[0] = "changed"
	clonedResult.ArtifactTags[0] = "changed"
	clonedResult.Messages[0].Media[0] = "changed"
	if result.Media[0] != "media" || result.ArtifactTags[0] != "artifact" ||
		result.Messages[0].Media[0] != "media" {
		t.Fatal("tool result clone retained mutable aliases")
	}

	cycle := p012CoverageCycle()
	fallbackClone := cloneStringAnyMap(cycle)
	if fallbackClone == nil || fallbackClone["self"] == nil {
		t.Fatal("public clone fallback dropped invalid compatibility data")
	}
	invalid := &ToolCallHookRequest{
		Tool:           "invalid",
		Arguments:      cycle,
		HookResult:     tools.NewToolResult("must be removed"),
		hookProvenance: tools.ToolHookProvenance{Name: "forged", Trusted: true},
	}
	sanitized := invalidToolCallHookRequest(invalid)
	if sanitized == invalid || len(sanitized.Arguments) != 0 || sanitized.HookResult != nil ||
		sanitized.policyHookProvenance().Trusted {
		t.Fatalf("invalid request was not sanitized: %#v", sanitized)
	}
	if invalidToolCallHookRequest(nil) != nil {
		t.Fatal("nil invalid request was not preserved")
	}

	if detached, err := detachLLMHookRequest(nil); detached != nil || err != nil {
		t.Fatalf("detach nil LLM request = (%#v, %v)", detached, err)
	}
	if detached, err := detachLLMHookResponse(nil); detached != nil || err != nil {
		t.Fatalf("detach nil LLM response = (%#v, %v)", detached, err)
	}
	if detached, err := detachToolCallHookRequest(nil); detached != nil || err != nil {
		t.Fatalf("detach nil tool request = (%#v, %v)", detached, err)
	}
	if detached, err := detachToolApprovalRequest(nil); detached != nil || err != nil {
		t.Fatalf("detach nil approval request = (%#v, %v)", detached, err)
	}
	if detached, err := detachToolResultHookResponse(nil); detached != nil || err != nil {
		t.Fatalf("detach nil tool response = (%#v, %v)", detached, err)
	}

	invalidCases := []struct {
		name string
		run  func() error
	}{
		{
			name: "LLM message tool call",
			run: func() error {
				_, err := detachLLMHookRequest(&LLMHookRequest{Messages: []providers.Message{{
					ToolCalls: []providers.ToolCall{{Arguments: p012CoverageCycle()}},
				}}})
				return err
			},
		},
		{
			name: "LLM tool definition",
			run: func() error {
				_, err := detachLLMHookRequest(&LLMHookRequest{Tools: []providers.ToolDefinition{
					p012CoverageDefinition("cycle", p012CoverageCycle()),
				}})
				return err
			},
		},
		{
			name: "LLM options",
			run: func() error {
				_, err := detachLLMHookRequest(&LLMHookRequest{Options: p012CoverageCycle()})
				return err
			},
		},
		{
			name: "LLM response",
			run: func() error {
				_, err := detachLLMHookResponse(&LLMHookResponse{Response: &providers.LLMResponse{
					ToolCalls: []providers.ToolCall{{Arguments: p012CoverageCycle()}},
				}})
				return err
			},
		},
		{
			name: "tool request arguments",
			run: func() error {
				_, err := detachToolCallHookRequest(&ToolCallHookRequest{Arguments: p012CoverageCycle()})
				return err
			},
		},
		{
			name: "tool request result",
			run: func() error {
				_, err := detachToolCallHookRequest(&ToolCallHookRequest{HookResult: &tools.ToolResult{
					Messages: []providers.Message{{ToolCalls: []providers.ToolCall{{Arguments: p012CoverageCycle()}}}},
				}})
				return err
			},
		},
		{
			name: "approval request",
			run: func() error {
				_, err := detachToolApprovalRequest(&ToolApprovalRequest{Arguments: p012CoverageCycle()})
				return err
			},
		},
		{
			name: "tool response arguments",
			run: func() error {
				_, err := detachToolResultHookResponse(&ToolResultHookResponse{Arguments: p012CoverageCycle()})
				return err
			},
		},
		{
			name: "tool response result",
			run: func() error {
				_, err := detachToolResultHookResponse(&ToolResultHookResponse{Result: &tools.ToolResult{
					Messages: []providers.Message{{ToolCalls: []providers.ToolCall{{Arguments: p012CoverageCycle()}}}},
				}})
				return err
			},
		},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("invalid detached graph was accepted")
			}
		})
	}
}

func TestP012HookCoverageManagerLifecycleAndValidation(t *testing.T) {
	var nilManager *HookManager
	nilManager.Close()
	nilManager.ConfigureTimeouts(time.Second, time.Second, time.Second)
	nilManager.Unmount("ignored")
	if err := nilManager.Mount(NamedHook("ignored", struct{}{})); err == nil {
		t.Fatal("nil manager accepted a mount")
	}

	manager := NewHookManager(nil)
	defer manager.Close()
	oldObserver := manager.observerTimeout
	manager.ConfigureTimeouts(0, -time.Second, 0)
	if manager.observerTimeout != oldObserver {
		t.Fatal("non-positive timeout changed manager configuration")
	}
	manager.ConfigureTimeouts(time.Second, 2*time.Second, 3*time.Second)
	if manager.observerTimeout != time.Second || manager.interceptorTimeout != 2*time.Second ||
		manager.approvalTimeout != 3*time.Second {
		t.Fatal("positive timeout configuration was not applied")
	}

	invalidRegistrations := []HookRegistration{
		{Name: "", Hook: struct{}{}},
		{Name: " spaced", Hook: struct{}{}},
		{Name: strings.Repeat("x", tools.MaxToolPolicyNameLen+1), Hook: struct{}{}},
		{Name: "control\nname", Hook: struct{}{}},
		{Name: "delete\x7fname", Hook: struct{}{}},
		{Name: "nil-hook"},
		{Name: "invalid-trust", Trust: HookTrust(99), Hook: struct{}{}},
	}
	for _, registration := range invalidRegistrations {
		if err := manager.Mount(registration); err == nil {
			t.Fatalf("invalid registration was accepted: %#v", registration)
		}
	}

	first := &p012CoverageCloser{}
	second := &p012CoverageCloser{err: errors.New("close fixture")}
	p012CoverageMount(t, manager, HookRegistration{
		Name: "replace", Trust: HookTrustTrusted, Hook: first,
	})
	p012CoverageMount(t, manager, HookRegistration{
		Name: "replace", Trust: HookTrustTrusted, Hook: second,
	})
	if first.calls != 1 {
		t.Fatalf("replaced hook close calls = %d", first.calls)
	}
	manager.Unmount("")
	manager.Unmount("missing")
	manager.Unmount("replace")
	if second.calls != 1 || len(manager.snapshotHooks()) != 0 {
		t.Fatalf("unmounted hook state = closes:%d hooks:%d", second.calls, len(manager.snapshotHooks()))
	}

	p012CoverageMount(t, manager, HookRegistration{
		Name: "priority-later", Priority: 20, Source: HookSourceInProcess, Hook: struct{}{},
	})
	p012CoverageMount(t, manager, HookRegistration{
		Name: "priority-first", Priority: 10, Source: HookSourceInProcess, Hook: struct{}{},
	})
	ordered := manager.snapshotHooks()
	if len(ordered) != 2 || ordered[0].Name != "priority-first" {
		t.Fatalf("priority ordering = %#v", ordered)
	}

	if provenance := trustedHookProvenance(UntrustedNamedHook("untrusted", struct{}{})); provenance.Trusted {
		t.Fatalf("untrusted registration gained provenance: %#v", provenance)
	}

	failingSubscribe := NewHookManager(p012CoverageEventChannel{err: errors.New("subscribe fixture")})
	failingSubscribe.Close()

	events := make(chan runtimeevents.Event)
	close(events)
	failingClose := NewHookManager(p012CoverageEventChannel{
		subscription: &p012CoverageSubscription{err: errors.New("subscription close fixture")},
		events:       events,
	})
	failingClose.Close()

	closeHookIfPossible(struct{}{})
	closeHookIfPossible(&p012CoverageCloser{err: errors.New("direct close fixture")})
}

func TestP012HookCoverageLLMTrustAndActionEdges(t *testing.T) {
	var nilManager *HookManager
	if got, decision := nilManager.BeforeLLM(context.Background(), nil); got != nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("nil BeforeLLM = (%#v, %#v)", got, decision)
	}
	if got, decision := nilManager.AfterLLM(context.Background(), nil); got != nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("nil AfterLLM = (%#v, %#v)", got, decision)
	}

	manager := NewHookManager(nil)
	invalidRequest := &LLMHookRequest{Options: p012CoverageCycle()}
	if got, decision := manager.BeforeLLM(context.Background(), invalidRequest); got == nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("invalid BeforeLLM = (%#v, %#v)", got, decision)
	}
	invalidResponse := &LLMHookResponse{Response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		Arguments: p012CoverageCycle(),
	}}}}
	if got, decision := manager.AfterLLM(context.Background(), invalidResponse); got == nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("invalid AfterLLM = (%#v, %#v)", got, decision)
	}
	manager.Close()

	beforeCases := []struct {
		name       string
		trust      HookTrust
		action     HookAction
		makeNext   func(*LLMHookRequest) *LLMHookRequest
		hookError  error
		wantModel  string
		wantAction HookAction
	}{
		{
			name: "untrusted modify", trust: HookTrustUntrusted, action: HookActionModify,
			makeNext: func(request *LLMHookRequest) *LLMHookRequest {
				next := request.Clone()
				next.Model = "untrusted-model"
				return next
			},
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "untrusted respond", trust: HookTrustUntrusted, action: HookActionRespond,
			makeNext:  func(request *LLMHookRequest) *LLMHookRequest { return request.Clone() },
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "trusted invalid mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(request *LLMHookRequest) *LLMHookRequest {
				next := request.Clone()
				next.Options = p012CoverageCycle()
				return next
			},
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "trusted nil mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:  func(*LLMHookRequest) *LLMHookRequest { return nil },
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "trusted respond unsupported", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext:  func(request *LLMHookRequest) *LLMHookRequest { return request.Clone() },
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "unknown action", trust: HookTrustTrusted, action: HookAction("unknown"),
			makeNext:  func(request *LLMHookRequest) *LLMHookRequest { return request.Clone() },
			wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "hook failure", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:  func(request *LLMHookRequest) *LLMHookRequest { return request.Clone() },
			hookError: errors.New("before fixture"), wantModel: "model-before", wantAction: HookActionContinue,
		},
		{
			name: "abort", trust: HookTrustTrusted, action: HookActionAbortTurn,
			makeNext:  func(request *LLMHookRequest) *LLMHookRequest { return request.Clone() },
			wantModel: "model-before", wantAction: HookActionAbortTurn,
		},
	}
	for _, test := range beforeCases {
		t.Run("before "+test.name, func(t *testing.T) {
			manager := NewHookManager(nil)
			defer manager.Close()
			hook := p012CoverageLLMHook{before: func(
				_ context.Context,
				request *LLMHookRequest,
			) (*LLMHookRequest, HookDecision, error) {
				return test.makeNext(request), HookDecision{Action: test.action}, test.hookError
			}}
			p012CoverageMount(t, manager, HookRegistration{
				Name: "llm-before", Trust: test.trust, Hook: hook,
			})
			got, decision := manager.BeforeLLM(context.Background(), p012CoverageLLMRequest())
			if got == nil || got.Model != test.wantModel || decision.Action != test.wantAction {
				t.Fatalf("BeforeLLM = (%#v, %#v)", got, decision)
			}
		})
	}

	afterCases := []struct {
		name        string
		trust       HookTrust
		action      HookAction
		makeNext    func(*LLMHookResponse) *LLMHookResponse
		hookError   error
		wantContent string
		wantAction  HookAction
	}{
		{
			name: "untrusted modify", trust: HookTrustUntrusted, action: HookActionModify,
			makeNext: func(response *LLMHookResponse) *LLMHookResponse {
				next := response.Clone()
				next.Response.Content = "untrusted-content"
				return next
			},
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "untrusted respond", trust: HookTrustUntrusted, action: HookActionRespond,
			makeNext:    func(response *LLMHookResponse) *LLMHookResponse { return response.Clone() },
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted invalid mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(response *LLMHookResponse) *LLMHookResponse {
				next := response.Clone()
				next.Response.ToolCalls[0].Arguments = p012CoverageCycle()
				return next
			},
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted nil mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:    func(*LLMHookResponse) *LLMHookResponse { return nil },
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted respond unsupported", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext:    func(response *LLMHookResponse) *LLMHookResponse { return response.Clone() },
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "unknown action", trust: HookTrustTrusted, action: HookAction("unknown"),
			makeNext:    func(response *LLMHookResponse) *LLMHookResponse { return response.Clone() },
			wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "hook failure", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:  func(response *LLMHookResponse) *LLMHookResponse { return response.Clone() },
			hookError: errors.New("after fixture"), wantContent: "original", wantAction: HookActionContinue,
		},
		{
			name: "hard abort", trust: HookTrustTrusted, action: HookActionHardAbort,
			makeNext:    func(response *LLMHookResponse) *LLMHookResponse { return response.Clone() },
			wantContent: "original", wantAction: HookActionHardAbort,
		},
	}
	for _, test := range afterCases {
		t.Run("after "+test.name, func(t *testing.T) {
			manager := NewHookManager(nil)
			defer manager.Close()
			hook := p012CoverageLLMHook{after: func(
				_ context.Context,
				response *LLMHookResponse,
			) (*LLMHookResponse, HookDecision, error) {
				return test.makeNext(response), HookDecision{Action: test.action}, test.hookError
			}}
			p012CoverageMount(t, manager, HookRegistration{
				Name: "llm-after", Trust: test.trust, Hook: hook,
			})
			got, decision := manager.AfterLLM(context.Background(), p012CoverageLLMResponse())
			if got == nil || got.Response == nil || got.Response.Content != test.wantContent ||
				decision.Action != test.wantAction {
				t.Fatalf("AfterLLM = (%#v, %#v)", got, decision)
			}
		})
	}

	trusted := NamedHook("provenance", struct{}{})
	prior := tools.ToolHookProvenance{Name: "prior", Source: "process", Trusted: true}
	current := &LLMHookResponse{
		Response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{
			{ID: "same", Name: "one"},
			{ID: "changed", Name: "old"},
		}},
		toolCallProvenance: []tools.ToolHookProvenance{prior},
	}
	next := &LLMHookResponse{Response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{
		{ID: "same", Name: "one"},
		{ID: "changed", Name: "new"},
	}}}
	provenance := afterLLMToolCallProvenance(trusted, current, next)
	if len(provenance) != 2 || provenance[0] != prior || provenance[1].Name != "provenance" ||
		!provenance[1].Trusted {
		t.Fatalf("AfterLLM provenance = %#v", provenance)
	}
	if got := afterLLMToolCallProvenance(trusted, nil, next); len(got) != 2 || !got[0].Trusted {
		t.Fatalf("nil-current provenance = %#v", got)
	}
	if afterLLMToolCallProvenance(trusted, current, nil) != nil {
		t.Fatal("nil next response gained provenance")
	}
	if got := (&LLMHookResponse{}).policyToolCallProvenance(); got != nil {
		t.Fatalf("empty provenance copy = %#v", got)
	}
	if got := (*LLMHookResponse)(nil).policyToolCallProvenance(); got != nil {
		t.Fatalf("nil provenance copy = %#v", got)
	}

	controlManager := NewHookManager(nil)
	defer controlManager.Close()
	if got := controlManager.applyBeforeLLMControls("nil-current", nil, p012CoverageLLMRequest()); got == nil {
		t.Fatal("nil-current control unexpectedly dropped next request")
	}
	if got := controlManager.applyBeforeLLMControls("nil-next", p012CoverageLLMRequest(), nil); got != nil {
		t.Fatalf("nil-next control = %#v", got)
	}
}

func TestP012HookCoverageToolTrustApprovalAndActionEdges(t *testing.T) {
	var nilManager *HookManager
	if got, decision := nilManager.BeforeTool(context.Background(), nil); got != nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("nil BeforeTool = (%#v, %#v)", got, decision)
	}
	if got, decision := nilManager.AfterTool(context.Background(), nil); got != nil ||
		decision.Action != HookActionContinue {
		t.Fatalf("nil AfterTool = (%#v, %#v)", got, decision)
	}
	if decision := nilManager.ApproveTool(context.Background(), nil); !decision.Approved {
		t.Fatalf("nil ApproveTool = %#v", decision)
	}

	manager := NewHookManager(nil)
	invalidRequest := &ToolCallHookRequest{
		Tool:           "unsafe",
		Arguments:      p012CoverageCycle(),
		HookResult:     tools.NewToolResult("remove"),
		hookProvenance: tools.ToolHookProvenance{Name: "forged", Trusted: true},
	}
	gotRequest, decision := manager.BeforeTool(context.Background(), invalidRequest)
	if gotRequest == nil || len(gotRequest.Arguments) != 0 || gotRequest.HookResult != nil ||
		gotRequest.policyHookProvenance().Trusted || decision.Action != HookActionDenyTool {
		t.Fatalf("invalid BeforeTool = (%#v, %#v)", gotRequest, decision)
	}
	invalidResponse := &ToolResultHookResponse{Arguments: p012CoverageCycle()}
	if got, afterDecision := manager.AfterTool(context.Background(), invalidResponse); got == nil ||
		afterDecision.Action != HookActionContinue {
		t.Fatalf("invalid AfterTool = (%#v, %#v)", got, afterDecision)
	}
	manager.Close()

	nonInterceptorManager := NewHookManager(nil)
	p012CoverageMount(t, nonInterceptorManager, NamedHook("not-a-tool-interceptor", struct{}{}))
	if got, afterDecision := nonInterceptorManager.AfterTool(
		context.Background(),
		&ToolResultHookResponse{},
	); got == nil ||
		afterDecision.Action != HookActionContinue {
		t.Fatalf("non-interceptor AfterTool = (%#v, %#v)", got, afterDecision)
	}
	nonInterceptorManager.Close()

	baseRequest := func() *ToolCallHookRequest {
		return &ToolCallHookRequest{Tool: "original", Arguments: map[string]any{"value": "original"}}
	}
	beforeCases := []struct {
		name        string
		trust       HookTrust
		action      HookAction
		makeNext    func(*ToolCallHookRequest) *ToolCallHookRequest
		hookError   error
		wantNil     bool
		wantTool    string
		wantAction  HookAction
		wantTrusted bool
	}{
		{
			name: "untrusted modify", trust: HookTrustUntrusted, action: HookActionModify,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest {
				next := request.Clone()
				next.Tool = "untrusted"
				return next
			},
			wantTool: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted continue changed", trust: HookTrustTrusted, action: HookActionContinue,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest {
				next := request.Clone()
				next.Tool = "trusted-changed"
				return next
			},
			wantTool: "trusted-changed", wantAction: HookActionContinue, wantTrusted: true,
		},
		{
			name: "trusted invalid mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest {
				next := request.Clone()
				next.Arguments = p012CoverageCycle()
				return next
			},
			wantTool: "original", wantAction: HookActionDenyTool,
		},
		{
			name: "trusted nil mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(*ToolCallHookRequest) *ToolCallHookRequest { return nil },
			wantTool: "original", wantAction: HookActionContinue,
		},
		{
			name: "untrusted respond", trust: HookTrustUntrusted, action: HookActionRespond,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest { return request.Clone() },
			wantTool: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted nil respond", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext: func(*ToolCallHookRequest) *ToolCallHookRequest { return nil },
			wantNil:  true, wantAction: HookActionRespond,
		},
		{
			name: "trusted invalid respond", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest {
				next := request.Clone()
				next.Arguments = p012CoverageCycle()
				return next
			},
			wantNil: true, wantAction: HookActionRespond,
		},
		{
			name: "trusted respond", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest {
				next := request.Clone()
				next.HookResult = tools.NewToolResult("synthetic")
				return next
			},
			wantTool: "original", wantAction: HookActionRespond, wantTrusted: true,
		},
		{
			name: "deny", trust: HookTrustUntrusted, action: HookActionDenyTool,
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest { return request.Clone() },
			wantTool: "original", wantAction: HookActionDenyTool,
		},
		{
			name: "unknown action", trust: HookTrustTrusted, action: HookAction("unknown"),
			makeNext: func(request *ToolCallHookRequest) *ToolCallHookRequest { return request.Clone() },
			wantTool: "original", wantAction: HookActionContinue,
		},
		{
			name: "hook failure", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:  func(request *ToolCallHookRequest) *ToolCallHookRequest { return request.Clone() },
			hookError: errors.New("tool before fixture"), wantTool: "original", wantAction: HookActionContinue,
		},
	}
	for _, test := range beforeCases {
		t.Run("before "+test.name, func(t *testing.T) {
			manager := NewHookManager(nil)
			defer manager.Close()
			hook := p012CoverageToolHook{before: func(
				_ context.Context,
				request *ToolCallHookRequest,
			) (*ToolCallHookRequest, HookDecision, error) {
				return test.makeNext(request), HookDecision{Action: test.action}, test.hookError
			}}
			p012CoverageMount(t, manager, HookRegistration{
				Name: "tool-before", Trust: test.trust, Hook: hook,
			})
			got, decision := manager.BeforeTool(context.Background(), baseRequest())
			if test.wantNil {
				if got != nil || decision.Action != test.wantAction {
					t.Fatalf("BeforeTool = (%#v, %#v)", got, decision)
				}
				return
			}
			if got == nil || got.Tool != test.wantTool || decision.Action != test.wantAction ||
				got.policyHookProvenance().Trusted != test.wantTrusted {
				t.Fatalf("BeforeTool = (%#v, %#v)", got, decision)
			}
		})
	}

	baseResponse := func() *ToolResultHookResponse {
		return &ToolResultHookResponse{
			Tool: "authorized", Arguments: map[string]any{"value": "authorized"},
			Result: tools.NewToolResult("original"),
		}
	}
	afterCases := []struct {
		name       string
		trust      HookTrust
		action     HookAction
		makeNext   func(*ToolResultHookResponse) *ToolResultHookResponse
		hookError  error
		wantResult string
		wantAction HookAction
	}{
		{
			name: "untrusted modify", trust: HookTrustUntrusted, action: HookActionModify,
			makeNext: func(response *ToolResultHookResponse) *ToolResultHookResponse {
				next := response.Clone()
				next.Result = tools.NewToolResult("untrusted")
				return next
			},
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(response *ToolResultHookResponse) *ToolResultHookResponse {
				next := response.Clone()
				next.Tool = "forged"
				next.Arguments = map[string]any{"value": "forged"}
				next.Result = tools.NewToolResult("trusted-result")
				return next
			},
			wantResult: "trusted-result", wantAction: HookActionContinue,
		},
		{
			name: "trusted invalid mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext: func(response *ToolResultHookResponse) *ToolResultHookResponse {
				next := response.Clone()
				next.Result = &tools.ToolResult{Messages: []providers.Message{{
					ToolCalls: []providers.ToolCall{{Arguments: p012CoverageCycle()}},
				}}}
				return next
			},
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted nil mutation", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:   func(*ToolResultHookResponse) *ToolResultHookResponse { return nil },
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "untrusted respond", trust: HookTrustUntrusted, action: HookActionRespond,
			makeNext:   func(response *ToolResultHookResponse) *ToolResultHookResponse { return response.Clone() },
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "trusted respond unsupported", trust: HookTrustTrusted, action: HookActionRespond,
			makeNext:   func(response *ToolResultHookResponse) *ToolResultHookResponse { return response.Clone() },
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "unknown action", trust: HookTrustTrusted, action: HookAction("unknown"),
			makeNext:   func(response *ToolResultHookResponse) *ToolResultHookResponse { return response.Clone() },
			wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "hook failure", trust: HookTrustTrusted, action: HookActionModify,
			makeNext:  func(response *ToolResultHookResponse) *ToolResultHookResponse { return response.Clone() },
			hookError: errors.New("tool after fixture"), wantResult: "original", wantAction: HookActionContinue,
		},
		{
			name: "abort", trust: HookTrustTrusted, action: HookActionAbortTurn,
			makeNext:   func(response *ToolResultHookResponse) *ToolResultHookResponse { return response.Clone() },
			wantResult: "original", wantAction: HookActionAbortTurn,
		},
	}
	for _, test := range afterCases {
		t.Run("after "+test.name, func(t *testing.T) {
			manager := NewHookManager(nil)
			defer manager.Close()
			hook := p012CoverageToolHook{after: func(
				_ context.Context,
				response *ToolResultHookResponse,
			) (*ToolResultHookResponse, HookDecision, error) {
				return test.makeNext(response), HookDecision{Action: test.action}, test.hookError
			}}
			p012CoverageMount(t, manager, HookRegistration{
				Name: "tool-after", Trust: test.trust, Hook: hook,
			})
			got, decision := manager.AfterTool(context.Background(), baseResponse())
			if got == nil || got.Result == nil || got.Result.ForLLM != test.wantResult ||
				got.Tool != "authorized" || got.Arguments["value"] != "authorized" ||
				decision.Action != test.wantAction {
				t.Fatalf("AfterTool = (%#v, %#v)", got, decision)
			}
		})
	}

	invalidApprovalManager := NewHookManager(nil)
	defer invalidApprovalManager.Close()
	p012CoverageMount(t, invalidApprovalManager, NamedHook("approver", p012CoverageApprover{
		approve: func(context.Context, *ToolApprovalRequest) (ApprovalDecision, error) {
			t.Fatal("approver received invalid arguments")
			return ApprovalDecision{}, nil
		},
	}))
	if approval := invalidApprovalManager.ApproveTool(context.Background(), &ToolApprovalRequest{
		Arguments: p012CoverageCycle(),
	}); approval.Approved || approval.Reason != "tool arguments are invalid" {
		t.Fatalf("invalid approval = %#v", approval)
	}

	failingApprovalManager := NewHookManager(nil)
	defer failingApprovalManager.Close()
	p012CoverageMount(t, failingApprovalManager, NamedHook("failing-approver", p012CoverageApprover{
		approve: func(context.Context, *ToolApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{}, errors.New("approval fixture")
		},
	}))
	if approval := failingApprovalManager.ApproveTool(context.Background(), &ToolApprovalRequest{
		Arguments: map[string]any{"value": "valid"},
	}); approval.Approved || !strings.Contains(approval.Reason, "failed") {
		t.Fatalf("failing approval = %#v", approval)
	}

	denyingApprovalManager := NewHookManager(nil)
	defer denyingApprovalManager.Close()
	p012CoverageMount(t, denyingApprovalManager, NamedHook("denying-approver", p012CoverageApprover{
		approve: func(context.Context, *ToolApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Approved: false, Reason: "fixture deny"}, nil
		},
	}))
	if approval := denyingApprovalManager.ApproveTool(
		context.Background(),
		&ToolApprovalRequest{},
	); approval.Approved ||
		approval.Reason != "fixture deny" {
		t.Fatalf("denying approval = %#v", approval)
	}
}

func TestP012HookCoverageRunnerObserverAndTimeoutEdges(t *testing.T) {
	value, decision, ok := runInterceptorHook(
		context.Background(),
		time.Second,
		"error",
		"coverage",
		func(context.Context) (string, HookDecision, error) {
			return "discard", HookDecision{Action: HookActionModify}, errors.New("interceptor fixture")
		},
	)
	if ok || value != "" || decision.Action != "" {
		t.Fatalf("error interceptor = (%q, %#v, %v)", value, decision, ok)
	}
	value, decision, ok = runInterceptorHook(
		context.Background(),
		time.Millisecond,
		"timeout",
		"coverage",
		func(ctx context.Context) (string, HookDecision, error) {
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond)
			return "late", HookDecision{Action: HookActionModify}, nil
		},
	)
	if ok || value != "" || decision.Action != "" {
		t.Fatalf("timeout interceptor = (%q, %#v, %v)", value, decision, ok)
	}

	approval, ok := runApprovalHook(
		context.Background(),
		time.Second,
		"error",
		"coverage",
		func(context.Context) (ApprovalDecision, error) {
			return ApprovalDecision{Approved: true}, errors.New("approval fixture")
		},
	)
	if ok || approval.Approved {
		t.Fatalf("error approval runner = (%#v, %v)", approval, ok)
	}
	approval, ok = runApprovalHook(
		context.Background(),
		time.Millisecond,
		"timeout",
		"coverage",
		func(ctx context.Context) (ApprovalDecision, error) {
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond)
			return ApprovalDecision{Approved: true}, nil
		},
	)
	if !ok || approval.Approved || !strings.Contains(approval.Reason, "timed out") {
		t.Fatalf("timeout approval runner = (%#v, %v)", approval, ok)
	}

	manager := NewHookManager(nil)
	defer manager.Close()
	manager.ConfigureTimeouts(time.Millisecond, 0, 0)
	manager.runRuntimeObserver("error-observer", p012CoverageObserver(func(
		context.Context,
		runtimeevents.Event,
	) error {
		return errors.New("observer fixture")
	}), runtimeevents.Event{})
	manager.runRuntimeObserver("timeout-observer", p012CoverageObserver(func(
		ctx context.Context,
		_ runtimeevents.Event,
	) error {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		return nil
	}), runtimeevents.Event{})
}

func TestP012ToolPolicyCoveragePreparationAndAuthorizationEdges(t *testing.T) {
	(*Pipeline)(nil).emitToolPolicyDecision(
		nil,
		nil,
		tools.ToolFulfillmentExecute,
		ToolPolicyOutcomeError,
		"guard",
	)

	if invocation, err := prepareAgentToolInvocation(nil, "missing", nil); invocation != nil ||
		!errors.Is(err, tools.ErrToolCatalogUnavailable) {
		t.Fatalf("nil execution preparation = (%#v, %v)", invocation, err)
	}
	if invocation, err := prepareAgentToolInvocation(&turnExecution{}, "missing", nil); invocation != nil ||
		!errors.Is(err, tools.ErrToolCatalogUnavailable) {
		t.Fatalf("nil catalog preparation = (%#v, %v)", invocation, err)
	}

	registry := tools.NewToolRegistry()
	registry.Register(p012CoverageTool{})
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog: %v", err)
	}
	definitions := catalog.ProviderDefinitions()
	execution := &turnExecution{toolCatalog: catalog, providerToolDefs: definitions}

	if invocation, prepareErr := prepareAgentToolInvocation(
		execution,
		"not-offered",
		nil,
	); invocation != nil || prepareErr == nil {
		t.Fatalf("not-offered preparation = (%#v, %v)", invocation, prepareErr)
	}
	unknownExecution := &turnExecution{
		toolCatalog: catalog,
		providerToolDefs: []providers.ToolDefinition{
			p012CoverageDefinition("unknown", map[string]any{"type": "object"}),
		},
	}
	if invocation, prepareErr := prepareAgentToolInvocation(
		unknownExecution,
		"unknown",
		nil,
	); invocation != nil ||
		prepareErr == nil {
		t.Fatalf("unknown catalog preparation = (%#v, %v)", invocation, prepareErr)
	}
	if invocation, prepareErr := prepareAgentToolInvocation(execution, p012CoverageTool{}.Name(), map[string]any{
		"unexpected": true,
	}); invocation != nil || prepareErr == nil {
		t.Fatalf("invalid registry arguments preparation = (%#v, %v)", invocation, prepareErr)
	}

	narrowDefinitions := catalog.ProviderDefinitions()
	narrowDefinitions[0].Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":   map[string]any{"type": "string"},
			"missing": map[string]any{"type": "string"},
		},
		"required":             []any{"missing"},
		"additionalProperties": false,
	}
	narrowExecution := &turnExecution{toolCatalog: catalog, providerToolDefs: narrowDefinitions}
	if invocation, prepareErr := prepareAgentToolInvocation(narrowExecution, p012CoverageTool{}.Name(), map[string]any{
		"value": "valid for registry only",
	}); invocation != nil || prepareErr == nil {
		t.Fatalf("narrow offered-schema preparation = (%#v, %v)", invocation, prepareErr)
	}

	invocation, err := prepareAgentToolInvocation(execution, p012CoverageTool{}.Name(), map[string]any{
		"value": "valid",
	})
	if err != nil || invocation == nil {
		t.Fatalf("valid preparation = (%#v, %v)", invocation, err)
	}
	pipeline := &Pipeline{}
	if authorization, authorizationErr := pipeline.authorizeAgentTool(
		context.Background(), nil, providers.ToolCall{ID: "call"}, invocation,
		tools.ToolFulfillmentExecute, tools.ToolHookProvenance{},
	); authorization.allowed || !errors.Is(authorizationErr, tools.ErrToolPolicyUnavailable) {
		t.Fatalf("nil turn authorization = (%#v, %v)", authorization, authorizationErr)
	}
	if authorization, authorizationErr := pipeline.authorizeAgentTool(
		context.Background(), &turnState{}, providers.ToolCall{ID: "call"}, nil,
		tools.ToolFulfillmentExecute, tools.ToolHookProvenance{},
	); authorization.allowed || !errors.Is(authorizationErr, tools.ErrToolPolicyUnavailable) {
		t.Fatalf("nil invocation authorization = (%#v, %v)", authorization, authorizationErr)
	}

	state := &turnState{
		agentID: "coverage-agent", sessionKey: "coverage-session", turnID: "coverage-turn",
		toolPolicy: tools.CompatibilityAllowToolPolicy{},
	}
	authorization, err := pipeline.authorizeAgentTool(
		context.Background(), state, providers.ToolCall{ID: "coverage-call"}, invocation,
		tools.ToolFulfillmentExecute, tools.ToolHookProvenance{},
	)
	if err != nil || !authorization.allowed || authorization.reasonCode != "policy_allowed" {
		t.Fatalf("compatibility authorization = (%#v, %v)", authorization, err)
	}

	state.toolPolicy = p012CoveragePolicyFunc(func(
		context.Context,
		tools.ToolPolicyRequest,
	) (tools.ToolPolicyDecision, error) {
		return tools.ToolPolicyDecision{Kind: tools.ToolPolicyDecisionDeny, ReasonCode: "coverage_deny"}, nil
	})
	authorization, err = pipeline.authorizeAgentTool(
		context.Background(), state, providers.ToolCall{ID: "coverage-call"}, invocation,
		tools.ToolFulfillmentExecute, tools.ToolHookProvenance{},
	)
	if err != nil || authorization.allowed || authorization.reasonCode != "policy_denied" ||
		authorization.message != toolPolicyDeniedMessage {
		t.Fatalf("policy denial authorization = (%#v, %v)", authorization, err)
	}

	approvalManager := NewHookManager(nil)
	defer approvalManager.Close()
	p012CoverageMount(t, approvalManager, NamedHook("authorization-approver", p012CoverageApprover{
		approve: func(context.Context, *ToolApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Approved: false, Reason: "deny"}, nil
		},
	}))
	pipeline.Hooks = approvalManager
	state.toolPolicy = tools.CompatibilityAllowToolPolicy{}
	authorization, err = pipeline.authorizeAgentTool(
		context.Background(), state, providers.ToolCall{ID: "coverage-call"}, invocation,
		tools.ToolFulfillmentExecute, tools.ToolHookProvenance{},
	)
	if err != nil || authorization.allowed || authorization.reasonCode != "approval_denied" ||
		authorization.message != toolApprovalDeniedMessage {
		t.Fatalf("approval denial authorization = (%#v, %v)", authorization, err)
	}

	if outcome, code := toolPolicyFailureOutcome(
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
	); outcome != ToolPolicyOutcomeCanceled ||
		code != "policy_canceled" {
		t.Fatalf("deadline policy outcome = (%q, %q)", outcome, code)
	}
}
