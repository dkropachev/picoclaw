package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestP008ReservedUpdatePlanIsOmittedFromEveryAdaptedSurface(t *testing.T) {
	defs := toolDefsForAdaptationTest(
		"exec",
		"exec_command",
		"write_stdin",
		"read_file",
		"write_file",
		"apply_patch",
		"load_image",
		"view_image",
		reservedUpdatePlanToolName,
	)
	tests := []struct {
		surface string
		want    []string
	}{
		{
			surface: config.ToolSurfacePicoClaw,
			want:    []string{"exec", "read_file", "write_file", "load_image"},
		},
		{
			surface: config.ToolSurfaceSimple,
			want:    []string{"exec", "read_file", "write_file", "load_image"},
		},
		{
			surface: config.ToolSurfaceCodex,
			want:    []string{"exec_command", "write_stdin", "read_file", "apply_patch", "view_image"},
		},
	}
	for _, test := range tests {
		t.Run(test.surface, func(t *testing.T) {
			got := toolDefNamesForAdaptationTest(applyToolAdaptationSurface(test.surface, defs))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("adapted tools = %v, want %v", got, test.want)
			}
		})
	}
	if !codexCompatibleToolName(reservedUpdatePlanToolName) {
		t.Fatal("reserved update_plan lost its Codex compatibility classification")
	}
}

func TestP008RuntimePromotionProjectionCannotReadvertiseUpdatePlan(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	model := fmt.Sprintf("p008-runtime-promotion-%d", time.Now().UnixNano())
	profile := tools.ToolAdaptationProfile{Provider: "local", Model: model}
	if _, ok := tools.ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceCodex,
		"exec_command",
		true,
		"",
		0,
	); !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome() ok = false, want true")
	}

	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ApplyVisibleChanges = config.ToolVisibleChangeImmediate
	cfg.Tools.Adaptation.CacheSensitiveAPIs = config.ToolCacheSensitivityNever
	ts := &turnState{agent: &AgentInstance{ToolAdaptation: tools.ToolAdaptationDecision{
		Enabled:             true,
		PinnedToolSurface:   config.ToolSurfacePicoClaw,
		VisibleToolSurface:  config.ToolSurfacePicoClaw,
		RuntimePromotion:    true,
		RuntimeDowngrade:    true,
		ApplyVisibleChanges: config.ToolVisibleChangeImmediate,
	}}}
	exec := &turnExecution{
		activeModelConfig: &config.ModelConfig{Provider: profile.Provider},
		activeModel:       profile.Model,
	}
	surface := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if surface != config.ToolSurfaceCodex {
		t.Fatalf("effective surface = %q, want %q", surface, config.ToolSurfaceCodex)
	}
	got := toolDefNamesForAdaptationTest(applyToolAdaptationSurface(
		surface,
		toolDefsForAdaptationTest("exec", "exec_command", reservedUpdatePlanToolName),
	))
	if want := []string{"exec_command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime-promotion tools = %v, want %v", got, want)
	}
}

type p008CaptureProvider struct {
	mu       sync.Mutex
	calls    int
	toolCall *providers.ToolCall
	tools    [][]string
	messages [][]providers.Message
}

func (p *p008CaptureProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.tools = append(p.tools, toolDefNamesForAdaptationTest(defs))
	p.messages = append(p.messages, cloneProviderMessages(messages))
	if p.calls == 1 && p.toolCall != nil {
		call := *p.toolCall
		call.Arguments = cloneStringAnyMap(p.toolCall.Arguments)
		return &providers.LLMResponse{
			ToolCalls:    []providers.ToolCall{call},
			FinishReason: "tool_calls",
		}, nil
	}
	content := "done"
	if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
		content = messages[len(messages)-1].Content
	}
	return &providers.LLMResponse{Content: content, FinishReason: "stop"}, nil
}

func (p *p008CaptureProvider) snapshot() (int, [][]string, [][]providers.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	toolNames := make([][]string, len(p.tools))
	for index := range p.tools {
		toolNames[index] = append([]string(nil), p.tools[index]...)
	}
	messages := make([][]providers.Message, len(p.messages))
	for index := range p.messages {
		messages[index] = cloneProviderMessages(p.messages[index])
	}
	return p.calls, toolNames, messages
}

type p008CountingTool struct {
	name  string
	calls atomic.Int64
}

func (tool *p008CountingTool) Name() string { return tool.name }

func (tool *p008CountingTool) Description() string { return "P008 dispatch canary" }

func (tool *p008CountingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p008CountingTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	tool.calls.Add(1)
	return tools.SilentResult("P008 canary executed")
}

type p008ToolRewriteHook struct {
	target string
	calls  atomic.Int64
}

func (hook *p008ToolRewriteHook) BeforeTool(
	_ context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	hook.calls.Add(1)
	next := call.Clone()
	if hook.target != "" {
		next.Tool = hook.target
		return next, HookDecision{Action: HookActionModify}, nil
	}
	return next, HookDecision{Action: HookActionContinue}, nil
}

func (hook *p008ToolRewriteHook) AfterTool(
	_ context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return result.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type p008AddPlanLLMHook struct {
	calls atomic.Int64
}

func (hook *p008AddPlanLLMHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	hook.calls.Add(1)
	next := req.Clone()
	next.Tools = append(next.Tools, toolDefsForAdaptationTest(reservedUpdatePlanToolName)...)
	return next, HookDecision{Action: HookActionModify}, nil
}

func (hook *p008AddPlanLLMHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func TestP008InitialAndBeforeLLMHookProjectionCannotAdvertiseUpdatePlan(t *testing.T) {
	provider := &p008CaptureProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&p008CountingTool{name: "echo_text"})
	al.RegisterTool(&p008CountingTool{name: reservedUpdatePlanToolName})
	hook := &p008AddPlanLLMHook{}
	if err := al.MountHook(NamedHook("p008-add-plan", hook)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}

	if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "p008-before-llm",
		UserMessage:     "inspect tools",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	}); err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if hook.calls.Load() != 1 {
		t.Fatalf("BeforeLLM calls = %d, want 1", hook.calls.Load())
	}
	_, toolSets, _ := provider.snapshot()
	if len(toolSets) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(toolSets))
	}
	if containsToolName(toolSets[0], reservedUpdatePlanToolName) {
		t.Fatalf("BeforeLLM projection advertised update_plan: %v", toolSets[0])
	}
}

func TestP008ProviderAuthoredUpdatePlanIsSkippedBeforeHooks(t *testing.T) {
	provider := &p008CaptureProvider{toolCall: &providers.ToolCall{
		ID:        "call-p008-plan",
		Name:      reservedUpdatePlanToolName,
		Arguments: map[string]any{"plan": []any{}},
	}}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	plan := &p008CountingTool{name: reservedUpdatePlanToolName}
	al.RegisterTool(plan)
	if result := agent.Tools.Execute(context.Background(), reservedUpdatePlanToolName, map[string]any{
		"plan": []any{},
	}); result.IsError {
		t.Fatalf("direct native plan call failed: %#v", result)
	}
	if plan.calls.Load() != 1 {
		t.Fatalf("direct native plan calls = %d, want 1", plan.calls.Load())
	}
	hook := &p008ToolRewriteHook{}
	if err := al.MountHook(NamedHook("p008-observe-tool", hook)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		8,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	response, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "p008-provider-plan",
		UserMessage:     "call plan",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	assertP008ReservedPlanSkip(t, response, provider, runtimeCh)
	if hook.calls.Load() != 0 {
		t.Fatalf("BeforeTool calls = %d, want reserved call rejected before hooks", hook.calls.Load())
	}
	if plan.calls.Load() != 1 {
		t.Fatalf("model-authored call reached native plan: calls=%d, want direct canary only", plan.calls.Load())
	}
}

func TestP008BeforeToolRewriteToUpdatePlanIsSkipped(t *testing.T) {
	provider := &p008CaptureProvider{toolCall: &providers.ToolCall{
		ID:        "call-p008-rewrite",
		Name:      "echo_text",
		Arguments: map[string]any{},
	}}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	echo := &p008CountingTool{name: "echo_text"}
	plan := &p008CountingTool{name: reservedUpdatePlanToolName}
	al.RegisterTool(echo)
	al.RegisterTool(plan)
	hook := &p008ToolRewriteHook{target: reservedUpdatePlanToolName}
	if err := al.MountHook(NamedHook("p008-rewrite-plan", hook)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		8,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	response, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "p008-hook-plan",
		UserMessage:     "rewrite tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	assertP008ReservedPlanSkip(t, response, provider, runtimeCh)
	if hook.calls.Load() != 1 {
		t.Fatalf("BeforeTool calls = %d, want 1", hook.calls.Load())
	}
	if echo.calls.Load() != 0 || plan.calls.Load() != 0 {
		t.Fatalf("hook rewrite dispatched a tool: echo=%d plan=%d", echo.calls.Load(), plan.calls.Load())
	}
}

func assertP008ReservedPlanSkip(
	t *testing.T,
	response string,
	provider *p008CaptureProvider,
	runtimeCh <-chan runtimeevents.Event,
) {
	t.Helper()
	if !strings.Contains(response, "durable plan support") {
		t.Fatalf("response = %q, want reserved-plan denial", response)
	}
	calls, toolSets, messages := provider.snapshot()
	if calls != 2 {
		t.Fatalf("provider calls = %d, want denial followed by one recovery call", calls)
	}
	for index, names := range toolSets {
		if containsToolName(names, reservedUpdatePlanToolName) {
			t.Fatalf("provider call %d advertised update_plan: %v", index, names)
		}
	}
	if len(messages) != 2 {
		t.Fatalf("captured message sets = %d, want 2", len(messages))
	}
	toolResults := 0
	for _, message := range messages[1] {
		if message.Role == "tool" && message.ToolCallID != "" {
			toolResults++
			if !strings.Contains(message.Content, "durable plan support") {
				t.Fatalf("skipped tool result = %q", message.Content)
			}
		}
	}
	if toolResults != 1 {
		t.Fatalf("skipped tool results = %d, want 1", toolResults)
	}
	events := collectRuntimeEventStream(runtimeCh)
	if len(events) != 1 {
		t.Fatalf("skipped events = %d, want 1: %#v", len(events), events)
	}
	payload, ok := events[0].Payload.(ToolExecSkippedPayload)
	if !ok {
		t.Fatalf("skipped payload = %T, want ToolExecSkippedPayload", events[0].Payload)
	}
	if payload.Tool != reservedUpdatePlanToolName ||
		!strings.Contains(payload.Reason, "durable plan support") {
		t.Fatalf("skipped payload = %#v", payload)
	}
}

func TestP008PlanOnlyProfileHasNoToolDefinitionOrToolUseRule(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		TurnProfile: config.TurnProfileConfig{
			Enabled: true,
			History: config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
			Tools: config.TurnProfileBlock{
				Mode:  config.TurnProfileModeCustom,
				Allow: []string{reservedUpdatePlanToolName},
			},
		},
	}}}
	provider := &turnProfileCaptureProvider{}
	al := newTurnProfileAgentLoop(t, cfg, provider)
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	for _, name := range agent.Tools.List() {
		agent.Tools.Unregister(name)
	}
	agent.Tools.Register(&p008CountingTool{name: reservedUpdatePlanToolName})

	if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "p008-plan-only-profile",
		UserMessage:     "plan work",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	}); err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if len(provider.tools) != 0 {
		t.Fatalf("plan-only provider tools = %#v, want none", provider.tools)
	}
	for _, message := range provider.messages {
		if message.Role == "system" && (strings.Contains(message.Content, toolUseSystemPromptRule()) ||
			strings.Contains(message.Content, "**ALWAYS use tools**")) {
			t.Fatalf("plan-only prompt contains impossible tool-use rule:\n%s", message.Content)
		}
	}
}

func TestP008DirectFactoryBackedUpdatePlanRemainsCallable(t *testing.T) {
	registry := tools.NewToolRegistry()
	if err := registry.RegisterFactoryBacked(
		tools.NewUpdatePlanTool(),
		tools.NewUpdatePlanToolFactory(),
	); err != nil {
		t.Fatalf("RegisterFactoryBacked() error = %v", err)
	}
	defer registry.Close()
	result := registry.Execute(context.Background(), reservedUpdatePlanToolName, map[string]any{
		"plan": []any{map[string]any{"step": "native", "status": "completed"}},
	})
	if result.IsError {
		t.Fatalf("direct factory-backed update_plan failed: %#v", result)
	}
}

func containsToolName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
