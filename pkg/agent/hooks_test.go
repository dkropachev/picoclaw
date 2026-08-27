package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func newHookTestLoop(
	t *testing.T,
	provider providers.LLMProvider,
) (*AgentLoop, *AgentInstance, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "agent-hooks-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "hook-model", Model: "hook-model"},
			{Name: "process-model", Model: "process-model"},
		},
	}

	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	return al, agent, func() {
		al.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

func TestHookManager_SortsInProcessBeforeProcess(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()

	if err := hm.Mount(HookRegistration{
		Name:     "process",
		Priority: -10,
		Source:   HookSourceProcess,
		Hook:     struct{}{},
	}); err != nil {
		t.Fatalf("mount process hook: %v", err)
	}
	if err := hm.Mount(HookRegistration{
		Name:     "in-process",
		Priority: 100,
		Source:   HookSourceInProcess,
		Hook:     struct{}{},
	}); err != nil {
		t.Fatalf("mount in-process hook: %v", err)
	}

	ordered := hm.snapshotHooks()
	if len(ordered) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(ordered))
	}
	if ordered[0].Name != "in-process" {
		t.Fatalf("expected in-process hook first, got %q", ordered[0].Name)
	}
	if ordered[1].Name != "process" {
		t.Fatalf("expected process hook second, got %q", ordered[1].Name)
	}
}

type llmHookTestProvider struct {
	mu        sync.Mutex
	lastModel string
}

func (p *llmHookTestProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.lastModel = model
	p.mu.Unlock()

	return &providers.LLMResponse{
		Content: "provider content",
	}, nil
}

type llmObserverHook struct {
	eventCh     chan runtimeevents.Event
	lastInbound *bus.InboundContext
	lastRoute   *routing.ResolvedRoute
	lastScope   *session.SessionScope
}

func (h *llmObserverHook) OnRuntimeEvent(ctx context.Context, evt runtimeevents.Event) error {
	if evt.Kind == runtimeevents.KindAgentTurnEnd {
		select {
		case h.eventCh <- evt:
		default:
		}
	}
	return nil
}

func (h *llmObserverHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	if req.Context != nil {
		h.lastInbound = cloneInboundContext(req.Context.Inbound)
		h.lastRoute = cloneResolvedRoute(req.Context.Route)
		h.lastScope = session.CloneScope(req.Context.Scope)
	}
	next := req.Clone()
	next.Model = "hook-model"
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *llmObserverHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	next := resp.Clone()
	next.Response.Content = "hooked content"
	return next, HookDecision{Action: HookActionModify}, nil
}

type dualRuntimeObserverHook struct {
	runtimeCh chan runtimeevents.Event
}

func (h *dualRuntimeObserverHook) OnRuntimeEvent(ctx context.Context, evt runtimeevents.Event) error {
	if evt.Kind == runtimeevents.KindAgentTurnEnd {
		select {
		case h.runtimeCh <- evt:
		default:
		}
	}
	return nil
}

type llmSystemRewriteHook struct{}

func (h *llmSystemRewriteHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Model = "changed-model"
	next.Messages[0].Content = "rewritten system"
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *llmSystemRewriteHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type llmUserAppendHook struct{}

func (h *llmUserAppendHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "extra user context"})
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *llmUserAppendHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type llmJSONRoundTripUserAppendHook struct{}

type jsonRoundTripLLMHookRequest struct {
	Model    string                     `json:"model"`
	Messages []providers.Message        `json:"messages,omitempty"`
	Tools    []providers.ToolDefinition `json:"tools,omitempty"`
}

func (h *llmJSONRoundTripUserAppendHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	payload := jsonRoundTripLLMHookRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Tools:    req.Tools,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, HookDecision{}, err
	}
	var decoded jsonRoundTripLLMHookRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, HookDecision{}, err
	}
	next := req.Clone()
	next.Model = decoded.Model
	next.Messages = decoded.Messages
	next.Tools = decoded.Tools
	next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "json extra user context"})
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *llmJSONRoundTripUserAppendHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp.Clone(), HookDecision{Action: HookActionContinue}, nil
}

type llmToolRewriteHook struct{}

func (h *llmToolRewriteHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Model = "changed-model"
	next.Tools[0].Function.Description = "rewritten tool"
	next.Tools = append(next.Tools, providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "hook_tool",
			Description: "hook tool",
			Parameters:  map[string]any{"type": "object"},
		},
		PromptLayer:  string(PromptLayerCapability),
		PromptSlot:   string(PromptSlotTooling),
		PromptSource: "hook:test",
	})
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *llmToolRewriteHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func TestHookManager_BeforeLLMControlsSystemPromptMutation(t *testing.T) {
	hm := NewHookManager(nil)
	if err := hm.Mount(NamedHook("rewrite-system", &llmSystemRewriteHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	req := &LLMHookRequest{
		Model: "original-model",
		Messages: []providers.Message{
			{
				Role:    "system",
				Content: "original system",
				SystemParts: []providers.ContentBlock{
					{Type: "text", Text: "original system"},
				},
			},
			{Role: "user", Content: "hello"},
		},
	}

	got, decision := hm.BeforeLLM(context.Background(), req)
	if decision.normalizedAction() != HookActionContinue {
		t.Fatalf("decision = %v, want continue", decision)
	}
	if got.Model != "changed-model" {
		t.Fatalf("model = %q, want changed-model", got.Model)
	}
	if got.Messages[0].Content != "original system" {
		t.Fatalf("system content = %q, want original system", got.Messages[0].Content)
	}
	if got.Messages[1].Content != "hello" {
		t.Fatalf("user content = %q, want hello", got.Messages[1].Content)
	}
}

func TestHookManager_BeforeLLMAllowsNonSystemMessageMutation(t *testing.T) {
	hm := NewHookManager(nil)
	if err := hm.Mount(NamedHook("append-user", &llmUserAppendHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	req := &LLMHookRequest{
		Model: "model",
		Messages: []providers.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
	}

	got, _ := hm.BeforeLLM(context.Background(), req)
	if len(got.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(got.Messages))
	}
	if got.Messages[2].Role != "user" || got.Messages[2].Content != "extra user context" {
		t.Fatalf("appended message = %#v, want extra user context", got.Messages[2])
	}
}

func TestHookManager_BeforeLLMAllowsJSONRoundTripNonSystemMessageMutation(t *testing.T) {
	hm := NewHookManager(nil)
	if err := hm.Mount(NamedHook("json-append-user", &llmJSONRoundTripUserAppendHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	req := &LLMHookRequest{
		Model: "model",
		Messages: []providers.Message{
			{
				Role:         "system",
				Content:      "system",
				PromptLayer:  string(PromptLayerKernel),
				PromptSlot:   string(PromptSlotIdentity),
				PromptSource: string(PromptSourceKernel),
				SystemParts: []providers.ContentBlock{
					{
						Type:         "text",
						Text:         "system",
						CacheControl: &providers.CacheControl{Type: "ephemeral"},
						PromptLayer:  string(PromptLayerKernel),
						PromptSlot:   string(PromptSlotIdentity),
						PromptSource: string(PromptSourceKernel),
					},
				},
			},
			{Role: "user", Content: "hello"},
		},
		Tools: []providers.ToolDefinition{
			{
				Type: "function",
				Function: providers.ToolFunctionDefinition{
					Name:        "mcp_github_create_issue",
					Description: "create issue",
					Parameters:  map[string]any{"type": "object"},
				},
				PromptLayer:  string(PromptLayerCapability),
				PromptSlot:   string(PromptSlotMCP),
				PromptSource: "mcp:github",
			},
		},
	}

	got, _ := hm.BeforeLLM(context.Background(), req)
	if len(got.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(got.Messages))
	}
	if got.Messages[2].Role != "user" || got.Messages[2].Content != "json extra user context" {
		t.Fatalf("appended message = %#v, want json extra user context", got.Messages[2])
	}
}

func TestHookManager_BeforeLLMControlsToolDefinitionMutation(t *testing.T) {
	hm := NewHookManager(nil)
	if err := hm.Mount(NamedHook("rewrite-tool", &llmToolRewriteHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	req := &LLMHookRequest{
		Model: "original-model",
		Messages: []providers.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
		Tools: []providers.ToolDefinition{
			{
				Type: "function",
				Function: providers.ToolFunctionDefinition{
					Name:        "mcp_github_create_issue",
					Description: "create issue",
					Parameters:  map[string]any{"type": "object"},
				},
				PromptLayer:  string(PromptLayerCapability),
				PromptSlot:   string(PromptSlotMCP),
				PromptSource: "mcp:github",
			},
		},
	}

	got, decision := hm.BeforeLLM(context.Background(), req)
	if decision.normalizedAction() != HookActionContinue {
		t.Fatalf("decision = %v, want continue", decision)
	}
	if got.Model != "changed-model" {
		t.Fatalf("model = %q, want changed-model", got.Model)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("tools len = %d, want original 1", len(got.Tools))
	}
	if got.Tools[0].Function.Description != "create issue" {
		t.Fatalf("tool description = %q, want original", got.Tools[0].Function.Description)
	}
	if got.Tools[0].PromptSource != "mcp:github" || got.Tools[0].PromptSlot != string(PromptSlotMCP) {
		t.Fatalf("tool prompt metadata = %#v, want original mcp metadata", got.Tools[0])
	}
}

func TestAgentLoop_Hooks_ObserverAndLLMInterceptor(t *testing.T) {
	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	hook := &llmObserverHook{eventCh: make(chan runtimeevents.Event, 1)}
	if err := al.MountHook(NamedHook("llm-observer", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		InboundContext: &bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "hook-user",
		},
		RouteResult: &routing.ResolvedRoute{
			AgentID:   "main",
			Channel:   "cli",
			AccountID: routing.DefaultAccountID,
			SessionPolicy: routing.SessionPolicy{
				Dimensions: []string{"sender"},
			},
			MatchedBy: "default",
		},
		SessionScope: &session.SessionScope{
			Version:    session.ScopeVersionV1,
			AgentID:    "main",
			Channel:    "cli",
			Account:    routing.DefaultAccountID,
			Dimensions: []string{"sender"},
			Values: map[string]string{
				"sender": "hook-user",
			},
		},
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "hooked content" {
		t.Fatalf("expected hooked content, got %q", resp)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "hook-model" {
		t.Fatalf("expected model hook-model, got %q", lastModel)
	}
	if hook.lastInbound == nil {
		t.Fatal("expected hook to receive inbound context")
	}
	if hook.lastInbound.Channel != "cli" || hook.lastInbound.SenderID != "hook-user" {
		t.Fatalf("hook inbound context = %+v", hook.lastInbound)
	}
	if hook.lastInbound != nil && hook.lastInbound.ChatID != "direct" {
		t.Fatalf("hook inbound chat ID = %q, want direct", hook.lastInbound.ChatID)
	}

	select {
	case evt := <-hook.eventCh:
		if evt.Kind != runtimeevents.KindAgentTurnEnd {
			t.Fatalf("expected turn end event, got %v", evt.Kind)
		}
		if evt.Scope.AgentID != "main" ||
			evt.Scope.SessionKey != "session-1" ||
			evt.Scope.Channel != "cli" ||
			evt.Scope.ChatID != "direct" ||
			evt.Scope.SenderID != "hook-user" {
			t.Fatalf("runtime observer scope = %+v", evt.Scope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hook observer event")
	}
}

func TestAgentLoop_Hooks_RuntimeObserverReceivesEvents(t *testing.T) {
	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	hook := &dualRuntimeObserverHook{
		runtimeCh: make(chan runtimeevents.Event, 1),
	}
	if err := al.MountHook(NamedHook("runtime-observer", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		InboundContext: &bus.InboundContext{
			Channel:   "cli",
			Account:   "default",
			ChatID:    "direct",
			ChatType:  "direct",
			SenderID:  "hook-user",
			MessageID: "msg-1",
		},
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "provider content" {
		t.Fatalf("expected provider content, got %q", resp)
	}

	select {
	case evt := <-hook.runtimeCh:
		if evt.Kind != runtimeevents.KindAgentTurnEnd {
			t.Fatalf("runtime observer kind = %q", evt.Kind)
		}
		if evt.Scope.SessionKey != "session-1" ||
			evt.Scope.Channel != "cli" ||
			evt.Scope.ChatID != "direct" ||
			evt.Scope.MessageID != "msg-1" {
			t.Fatalf("runtime observer scope = %+v", evt.Scope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime observer event")
	}
}

func TestAgentLoop_BtwCommand_UsesLLMHooks(t *testing.T) {
	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	useTestSideQuestionProvider(al, provider)

	hook := &llmObserverHook{eventCh: make(chan runtimeevents.Event, 1)}
	if err := al.MountHook(NamedHook("llm-observer", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	response, handled := al.handleCommand(context.Background(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "hook-user",
		},
		Content: "/btw hello",
	}, agent, &processOptions{
		Dispatch: DispatchRequest{
			SessionKey: "session-1",
			InboundContext: &bus.InboundContext{
				Channel:  "cli",
				ChatID:   "direct",
				ChatType: "direct",
				SenderID: "hook-user",
			},
			RouteResult: &routing.ResolvedRoute{
				AgentID:   "main",
				Channel:   "cli",
				AccountID: routing.DefaultAccountID,
				SessionPolicy: routing.SessionPolicy{
					Dimensions: []string{"sender"},
				},
				MatchedBy: "default",
			},
			SessionScope: &session.SessionScope{
				Version:    session.ScopeVersionV1,
				AgentID:    "main",
				Channel:    "cli",
				Account:    routing.DefaultAccountID,
				Dimensions: []string{"sender"},
				Values: map[string]string{
					"sender": "hook-user",
				},
			},
			UserMessage: "/btw hello",
		},
		SessionKey:        "session-1",
		Channel:           "cli",
		ChatID:            "direct",
		SenderID:          "hook-user",
		SenderDisplayName: "Hook User",
	})
	if !handled {
		t.Fatal("expected /btw command to be handled")
	}
	if response != "hooked content" {
		t.Fatalf("expected hooked content, got %q", response)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "hook-model" {
		t.Fatalf("expected model hook-model, got %q", lastModel)
	}
	if hook.lastInbound == nil {
		t.Fatal("expected hook to receive inbound context")
	}
	if hook.lastInbound.Channel != "cli" || hook.lastInbound.SenderID != "hook-user" {
		t.Fatalf("hook inbound context = %+v", hook.lastInbound)
	}
	if hook.lastInbound.ChatID != "direct" {
		t.Fatalf("hook inbound chat ID = %q, want direct", hook.lastInbound.ChatID)
	}
	if hook.lastRoute == nil || hook.lastRoute.AgentID != "main" {
		t.Fatalf("expected hook route context for /btw, got %+v", hook.lastRoute)
	}
	if hook.lastScope == nil || hook.lastScope.Values["sender"] != "hook-user" {
		t.Fatalf("expected hook session scope for /btw, got %+v", hook.lastScope)
	}
}

type toolHookProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *toolHookProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++
	if p.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{
					ID:        "call-1",
					Name:      "echo_text",
					Arguments: map[string]any{"text": "original"},
				},
			},
		}, nil
	}

	last := messages[len(messages)-1]
	return &providers.LLMResponse{
		Content: last.Content,
	}, nil
}

type echoTextTool struct{}

func (t *echoTextTool) Name() string {
	return "echo_text"
}

func (t *echoTextTool) Description() string {
	return "echo a text argument"
}

func (t *echoTextTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type": "string",
			},
		},
	}
}

func (t *echoTextTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	text, _ := args["text"].(string)
	return tools.SilentResult(text)
}

type toolRewriteHook struct{}

func (h *toolRewriteHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	next := call.Clone()
	next.Arguments["text"] = "modified"
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *toolRewriteHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	next := result.Clone()
	next.Result.ForLLM = "after:" + next.Result.ForLLM
	return next, HookDecision{Action: HookActionModify}, nil
}

type toolRenameHook struct{}

func (h *toolRenameHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	next := call.Clone()
	next.Tool = "echo_text_rewritten"
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h *toolRenameHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return result.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func TestAgentLoop_Hooks_ToolInterceptorCanRewrite(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&echoTextTool{})
	if err := al.MountHook(NamedHook("tool-rewrite", &toolRewriteHook{})); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "after:modified" {
		t.Fatalf("expected rewritten tool result, got %q", resp)
	}
}

type echoTextRewrittenTool struct{}

func (t *echoTextRewrittenTool) Name() string {
	return "echo_text_rewritten"
}

func (t *echoTextRewrittenTool) Description() string {
	return "echo a rewritten text argument"
}

func (t *echoTextRewrittenTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type": "string",
			},
		},
	}
}

func (t *echoTextRewrittenTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	text, _ := args["text"].(string)
	return tools.SilentResult("rewritten:" + text)
}

func TestAgentLoop_Hooks_ToolFeedbackUsesRewrittenToolName(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.cfg.Agents.Defaults.ToolFeedback.Enabled = true
	al.RegisterTool(&echoTextTool{})
	al.RegisterTool(&echoTextRewrittenTool{})
	if err := al.MountHook(NamedHook("tool-rename", &toolRenameHook{})); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	_, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	msgBus, ok := al.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("expected concrete MessageBus, got %T", al.bus)
	}

	select {
	case outbound := <-msgBus.OutboundChan():
		if !strings.Contains(outbound.Content, "`echo_text_rewritten`") {
			t.Fatalf("tool feedback content = %q, want rewritten tool name", outbound.Content)
		}
		if strings.Contains(outbound.Content, "`echo_text`") {
			t.Fatalf("tool feedback content = %q, want no original tool name", outbound.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected outbound tool feedback")
	}
}

type denyApprovalHook struct{}

func (h *denyApprovalHook) ApproveTool(ctx context.Context, req *ToolApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{
		Approved: false,
		Reason:   "blocked",
	}, nil
}

func TestAgentLoop_Hooks_ToolApproverCanDeny(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&echoTextTool{})
	if err := al.MountHook(NamedHook("deny-approval", &denyApprovalHook{})); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	expected := toolApprovalDeniedMessage
	if resp != expected {
		t.Fatalf("expected %q, got %q", expected, resp)
	}

	events := collectRuntimeEventStream(runtimeCh)
	skippedEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentToolExecSkipped)
	if !ok {
		t.Fatal("expected tool skipped event")
	}
	payload, ok := skippedEvt.Payload.(ToolExecSkippedPayload)
	if !ok {
		t.Fatalf("expected ToolExecSkippedPayload, got %T", skippedEvt.Payload)
	}
	if payload.Reason != expected {
		t.Fatalf("expected skipped reason %q, got %q", expected, payload.Reason)
	}
}

// respondHook is a test hook for testing HookActionRespond functionality
type respondHook struct {
	respondTools map[string]bool // tool names to respond to
}

type steeringRespondHook struct {
	respondHook
	steer    func() error
	steerErr error
}

func (h *steeringRespondHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if h.respondTools[call.Tool] && h.steer != nil {
		h.steerErr = h.steer()
	}
	return h.respondHook.BeforeTool(ctx, call)
}

func (h *respondHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if h.respondTools[call.Tool] {
		next := call.Clone()
		next.HookResult = &tools.ToolResult{
			ForLLM:  "hook-responded: " + call.Tool,
			ForUser: "",
			Silent:  false,
			IsError: false,
		}
		return next, HookDecision{Action: HookActionRespond}, nil
	}
	return call, HookDecision{Action: HookActionContinue}, nil
}

func (h *respondHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	// Should not be called since respond skips tool execution
	return result, HookDecision{Action: HookActionContinue}, nil
}

func TestAgentLoop_Hooks_ToolRespondAction(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&echoTextTool{})
	if err := al.MountHook(NamedHook("respond-hook", &respondHook{
		respondTools: map[string]bool{"echo_text": true},
	})); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentToolExecEnd,
	)
	defer closeRuntimeEvents()

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	// Verify response comes from hook, not tool
	expected := "hook-responded: echo_text"
	if resp != expected {
		t.Fatalf("expected %q, got %q", expected, resp)
	}

	// Verify event stream has ToolExecEnd, not actual tool execution
	events := collectRuntimeEventStream(runtimeCh)
	endEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentToolExecEnd)
	if !ok {
		t.Fatal("expected tool exec end event")
	}
	payload, ok := endEvt.Payload.(ToolExecEndPayload)
	if !ok {
		t.Fatalf("expected ToolExecEndPayload, got %T", endEvt.Payload)
	}
	if payload.Tool != "echo_text" {
		t.Fatalf("expected tool echo_text, got %q", payload.Tool)
	}
	if payload.ForLLMLen != len(expected) {
		t.Fatalf("expected ForLLMLen %d, got %d", len(expected), payload.ForLLMLen)
	}
}

// denyToolHook tests HookActionDenyTool functionality
type denyToolHook struct {
	denyTools map[string]bool
}

func (h *denyToolHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if h.denyTools[call.Tool] {
		return call, HookDecision{Action: HookActionDenyTool, Reason: "tool denied by hook"}, nil
	}
	return call, HookDecision{Action: HookActionContinue}, nil
}

func (h *denyToolHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return result, HookDecision{Action: HookActionContinue}, nil
}

func TestAgentLoop_Hooks_ToolDenyAction(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&echoTextTool{})
	if err := al.MountHook(NamedHook("deny-hook", &denyToolHook{
		denyTools: map[string]bool{"echo_text": true},
	})); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	expected := "Tool execution denied by hook: tool denied by hook"
	if resp != expected {
		t.Fatalf("expected %q, got %q", expected, resp)
	}
}

func TestHookManager_BeforeTool_RespondAction(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()

	hook := &respondHook{
		respondTools: map[string]bool{"test_tool": true},
	}
	if err := hm.Mount(NamedHook("respond-test", hook)); err != nil {
		t.Fatalf("mount hook: %v", err)
	}

	req := &ToolCallHookRequest{
		Tool:      "test_tool",
		Arguments: map[string]any{"arg": "value"},
	}
	result, decision := hm.BeforeTool(context.Background(), req)

	if decision.Action != HookActionRespond {
		t.Fatalf("expected action %q, got %q", HookActionRespond, decision.Action)
	}

	if result.HookResult == nil {
		t.Fatal("expected HookResult to be set")
	}
	if result.HookResult.ForLLM != "hook-responded: test_tool" {
		t.Fatalf("unexpected HookResult.ForLLM: %q", result.HookResult.ForLLM)
	}
}

type respondWithMediaHook struct {
	respondTools    map[string]bool
	media           []string
	responseHandled bool
	forLLM          string
}

func (h *respondWithMediaHook) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if h.respondTools[call.Tool] {
		next := call.Clone()
		next.HookResult = &tools.ToolResult{
			ForLLM:          h.forLLM,
			ForUser:         "media result",
			Media:           h.media,
			ResponseHandled: h.responseHandled,
			Silent:          false,
			IsError:         false,
		}
		return next, HookDecision{Action: HookActionRespond}, nil
	}
	return call, HookDecision{Action: HookActionContinue}, nil
}

func (h *respondWithMediaHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return result, HookDecision{Action: HookActionContinue}, nil
}

type errorMediaChannel struct {
	fakeChannel
	sendErr error
}

func (f *errorMediaChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	return nil, f.sendErr
}

func TestAgentLoop_HookRespond_MediaError(t *testing.T) {
	provider := &multiToolProvider{
		toolCalls: []providers.ToolCall{
			{ID: "call-1", Name: "media_tool", Arguments: map[string]any{}},
		},
		finalContent: "done",
	}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&slowTool{name: "media_tool"})

	hook := &respondWithMediaHook{
		respondTools:    map[string]bool{"media_tool": true},
		media:           []string{"media://test/image.png"},
		responseHandled: true,
		forLLM:          "media sent successfully",
	}
	if err := al.MountHook(NamedHook("media-hook", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	al.channelManager = newStartedTestChannelManager(t,
		al.bus.(*bus.MessageBus), al.mediaStore, "discord", &errorMediaChannel{
			sendErr: errors.New("channel unavailable"),
		})

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentToolExecEnd,
	)
	defer closeRuntimeEvents()

	_, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-media-err",
		Channel:         "discord",
		ChatID:          "chat1",
		UserMessage:     "send media",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	events := collectRuntimeEventStream(runtimeCh)
	endEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentToolExecEnd)
	if !ok {
		t.Fatal("expected ToolExecEnd event")
	}
	payload, ok := endEvt.Payload.(ToolExecEndPayload)
	if !ok {
		t.Fatalf("expected ToolExecEndPayload, got %T", endEvt.Payload)
	}

	if !payload.IsError {
		t.Fatal("expected IsError=true when SendMedia fails")
	}

	if payload.ForLLMLen < 30 {
		t.Fatalf("expected ForLLM to contain error message, got ForLLMLen=%d", payload.ForLLMLen)
	}
}

func TestAgentLoop_HookRespond_BusFallback(t *testing.T) {
	provider := &multiToolProvider{
		toolCalls: []providers.ToolCall{
			{ID: "call-1", Name: "media_tool", Arguments: map[string]any{}},
		},
		finalContent: "done",
	}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&slowTool{name: "media_tool"})

	hook := &respondWithMediaHook{
		respondTools:    map[string]bool{"media_tool": true},
		media:           []string{"media://test/image.png"},
		responseHandled: true,
		forLLM:          "media queued",
	}
	if err := al.MountHook(NamedHook("media-hook", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentToolExecEnd,
	)
	defer closeRuntimeEvents()

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-bus-fallback",
		Channel:         "cli",
		ChatID:          "chat1",
		UserMessage:     "send media",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	events := collectRuntimeEventStream(runtimeCh)
	endEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentToolExecEnd)
	if !ok {
		t.Fatal("expected ToolExecEnd event")
	}
	payload, ok := endEvt.Payload.(ToolExecEndPayload)
	if !ok {
		t.Fatalf("expected ToolExecEndPayload, got %T", endEvt.Payload)
	}

	if payload.IsError {
		t.Fatal("expected IsError=false for bus fallback (media queued, not delivered)")
	}

	if resp != "done" {
		t.Fatalf("expected response 'done', got %q", resp)
	}
}

func TestAgentLoop_HookRespond_ResponseHandledMediaPreservesOutboundContext(t *testing.T) {
	provider := &multiToolProvider{
		toolCalls: []providers.ToolCall{
			{ID: "call-1", Name: "media_tool", Arguments: map[string]any{}},
		},
		finalContent: "done",
	}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&slowTool{name: "media_tool"})

	hook := &respondWithMediaHook{
		respondTools:    map[string]bool{"media_tool": true},
		media:           []string{"media://test/image.png"},
		responseHandled: true,
		forLLM:          "media sent successfully",
	}
	if err := al.MountHook(NamedHook("media-hook", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	telegramChannel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "rid-telegram"}}
	al.channelManager = newStartedTestChannelManager(t,
		al.bus.(*bus.MessageBus), al.mediaStore, "telegram", telegramChannel)

	_, err := al.runAgentLoop(context.Background(), agent, processOptions{
		Dispatch: DispatchRequest{
			SessionKey: "session-topic-media",
			SessionScope: &session.SessionScope{
				Version:    session.ScopeVersionV1,
				AgentID:    agent.ID,
				Channel:    "telegram",
				Dimensions: []string{"chat"},
				Values: map[string]string{
					"chat": "forum:-100123/42",
				},
			},
			InboundContext: &bus.InboundContext{
				Channel:  "telegram",
				ChatID:   "-100123",
				TopicID:  "42",
				ChatType: "group",
				SenderID: "user1",
			},
			UserMessage: "send media",
		},
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	if len(telegramChannel.sentMedia) != 1 {
		t.Fatalf("expected exactly 1 sent media message, got %d", len(telegramChannel.sentMedia))
	}
	sent := telegramChannel.sentMedia[0]
	if sent.Context.Channel != "telegram" || sent.Context.ChatID != "-100123" || sent.Context.TopicID != "42" {
		t.Fatalf("unexpected media context: %+v", sent.Context)
	}
	if sent.AgentID != agent.ID {
		t.Fatalf("sent media agent_id = %q, want %q", sent.AgentID, agent.ID)
	}
	if sent.SessionKey != "session-topic-media" {
		t.Fatalf("sent media session_key = %q, want session-topic-media", sent.SessionKey)
	}
	if sent.Scope == nil || sent.Scope.Values["chat"] != "forum:-100123/42" {
		t.Fatalf("unexpected sent media scope: %+v", sent.Scope)
	}
}

type multiToolProvider struct {
	mu           sync.Mutex
	callCount    int
	toolCalls    []providers.ToolCall
	finalContent string
}

func (p *multiToolProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.callCount++
	if p.callCount == 1 && len(p.toolCalls) > 0 {
		return &providers.LLMResponse{
			ToolCalls: p.toolCalls,
		}, nil
	}

	return &providers.LLMResponse{
		Content: p.finalContent,
	}, nil
}

func TestAgentLoop_HookRespond_InterruptSkipsRemaining(t *testing.T) {
	provider := &multiToolProvider{
		toolCalls: []providers.ToolCall{
			{ID: "call-1", Name: "tool_one", Arguments: map[string]any{}},
			{ID: "call-2", Name: "tool_two", Arguments: map[string]any{}},
			{ID: "call-3", Name: "tool_three", Arguments: map[string]any{}},
		},
		finalContent: "done",
	}
	al, _, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&slowTool{name: "tool_one"})
	tool1ExecCh := make(chan struct{}, 1)
	al.RegisterTool(&slowTool{name: "tool_two", duration: 100 * time.Millisecond, execCh: tool1ExecCh})
	al.RegisterTool(&slowTool{name: "tool_three", duration: 100 * time.Millisecond})

	hook := &respondHook{
		respondTools: map[string]bool{"tool_one": true},
	}
	if err := al.MountHook(NamedHook("respond-hook", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		32,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	sessionKey := session.BuildMainSessionKey(routing.DefaultAgentID)

	type result struct {
		resp string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := al.ProcessDirectWithChannel(
			context.Background(),
			"run tools",
			sessionKey,
			"cli",
			"chat1",
		)
		resultCh <- result{resp: resp, err: err}
	}()

	select {
	case <-tool1ExecCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for tool execution to start")
	}

	if err := al.InterruptGraceful("stop now"); err != nil {
		t.Fatalf("InterruptGraceful failed: %v", err)
	}

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for result")
	}

	events := collectRuntimeEventStream(runtimeCh)

	skippedEvts := filterRuntimeEvents(events, runtimeevents.KindAgentToolExecSkipped)
	if len(skippedEvts) < 1 {
		t.Fatal("expected at least one ToolExecSkipped event after interrupt")
	}

	for _, evt := range skippedEvts {
		payload, ok := evt.Payload.(ToolExecSkippedPayload)
		if !ok {
			t.Fatalf("expected ToolExecSkippedPayload, got %T", evt.Payload)
		}
		if payload.Reason != "graceful interrupt requested" {
			t.Fatalf("expected skip reason 'graceful interrupt requested', got %q", payload.Reason)
		}
	}
}

func TestAgentLoop_HookRespond_SteeringSkipsRemaining(t *testing.T) {
	provider := &multiToolProvider{
		toolCalls: []providers.ToolCall{
			{ID: "call-1", Name: "tool_one", Arguments: map[string]any{}},
			{ID: "call-2", Name: "tool_two", Arguments: map[string]any{}},
			{ID: "call-3", Name: "tool_three", Arguments: map[string]any{}},
		},
		finalContent: "done",
	}
	al, _, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&slowTool{name: "tool_one"})
	al.RegisterTool(&slowTool{name: "tool_two", duration: 100 * time.Millisecond})
	al.RegisterTool(&slowTool{name: "tool_three", duration: 100 * time.Millisecond})

	hook := &steeringRespondHook{
		respondHook: respondHook{
			respondTools: map[string]bool{"tool_one": true},
		},
		steer: func() error {
			return al.Steer(providers.Message{Role: "user", Content: "change direction"})
		},
	}
	if err := al.MountHook(NamedHook("respond-hook", hook)); err != nil {
		t.Fatalf("MountHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		32,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	sessionKey := session.BuildMainSessionKey(routing.DefaultAgentID)
	if _, err := al.ProcessDirectWithChannel(
		context.Background(),
		"run tools",
		sessionKey,
		"cli",
		"chat1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook.steerErr != nil {
		t.Fatalf("synchronous steering failed: %v", hook.steerErr)
	}

	events := collectRuntimeEventStream(runtimeCh)

	skippedEvts := filterRuntimeEvents(events, runtimeevents.KindAgentToolExecSkipped)
	if len(skippedEvts) != 2 {
		t.Fatalf("skipped event count = %d, want 2", len(skippedEvts))
	}

	skippedTools := make(map[string]bool, len(skippedEvts))
	for _, evt := range skippedEvts {
		payload, ok := evt.Payload.(ToolExecSkippedPayload)
		if !ok {
			t.Fatalf("expected ToolExecSkippedPayload, got %T", evt.Payload)
		}
		if payload.Reason != "queued user steering message" {
			t.Fatalf("expected skip reason 'queued user steering message', got %q", payload.Reason)
		}
		skippedTools[payload.Tool] = true
	}
	for _, toolName := range []string{"tool_two", "tool_three"} {
		if !skippedTools[toolName] {
			t.Fatalf("missing skipped event for %s: %#v", toolName, skippedTools)
		}
	}
}

func TestCloneStringAnyMap_EmptyMapReturnsNonNil(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		wantNil bool
		wantLen int
	}{
		{
			name:    "nil input returns empty map",
			input:   nil,
			wantNil: false,
			wantLen: 0,
		},
		{
			name:    "empty map returns empty map",
			input:   map[string]any{},
			wantNil: false,
			wantLen: 0,
		},
		{
			name:    "populated map is cloned",
			input:   map[string]any{"key": "value"},
			wantNil: false,
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cloneStringAnyMap(tt.input)
			if result == nil {
				t.Fatal("cloneStringAnyMap returned nil — MCP tool calls " +
					"with no arguments would send null instead of {}")
			}
			if len(result) != tt.wantLen {
				t.Fatalf("expected len %d, got %d", tt.wantLen, len(result))
			}
		})
	}

	t.Run("clone does not share underlying map", func(t *testing.T) {
		src := map[string]any{"a": 1}
		cloned := cloneStringAnyMap(src)
		cloned["b"] = 2
		if _, ok := src["b"]; ok {
			t.Fatal("modifying clone should not affect source")
		}
	})

	t.Run("clone deeply detaches nested maps and slices", func(t *testing.T) {
		src := map[string]any{
			"nested": map[string]any{
				"items": []any{map[string]any{"value": "original"}},
			},
		}
		cloned := cloneStringAnyMap(src)
		clonedNested := cloned["nested"].(map[string]any)
		clonedItems := clonedNested["items"].([]any)
		clonedItems[0].(map[string]any)["value"] = "changed"

		originalNested := src["nested"].(map[string]any)
		originalItems := originalNested["items"].([]any)
		if got := originalItems[0].(map[string]any)["value"]; got != "original" {
			t.Fatalf("nested source value = %v, want original", got)
		}
	})
}

type untrustedMutationHook struct{}

func (untrustedMutationHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	req.Model = "untrusted-model"
	req.Options["nested"].(map[string]any)["value"] = "untrusted"
	return req, HookDecision{Action: HookActionModify}, nil
}

func (untrustedMutationHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	resp.Response.Content = "untrusted-content"
	resp.Response.ToolCalls[0].Arguments["nested"].(map[string]any)["value"] = "untrusted"
	return resp, HookDecision{Action: HookActionModify}, nil
}

func (untrustedMutationHook) BeforeTool(
	_ context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	call.Tool = "untrusted_tool"
	call.Arguments["nested"].(map[string]any)["value"] = "untrusted"
	call.HookResult = tools.SilentResult("untrusted response")
	return call, HookDecision{Action: HookActionRespond}, nil
}

func (untrustedMutationHook) AfterTool(
	_ context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	result.Tool = "untrusted_tool"
	result.Arguments["nested"].(map[string]any)["value"] = "untrusted"
	result.Result.ForLLM = "untrusted result"
	return result, HookDecision{Action: HookActionModify}, nil
}

func TestHookManager_UntrustedMutationsAndRespondAreDiscarded(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()
	if err := hm.Mount(UntrustedNamedHook("untrusted", untrustedMutationHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	llmRequest := &LLMHookRequest{
		Model:   "original-model",
		Options: map[string]any{"nested": map[string]any{"value": "original"}},
	}
	gotRequest, requestDecision := hm.BeforeLLM(context.Background(), llmRequest)
	if requestDecision.normalizedAction() != HookActionContinue || gotRequest.Model != "original-model" {
		t.Fatalf("BeforeLLM = %#v / %#v, want unchanged continue", gotRequest, requestDecision)
	}
	if got := gotRequest.Options["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("BeforeLLM nested value = %v, want original", got)
	}

	llmResponse := &LLMHookResponse{Response: &providers.LLMResponse{
		Content: "original-content",
		ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Name:      "original_tool",
			Arguments: map[string]any{"nested": map[string]any{"value": "original"}},
		}},
	}}
	gotResponse, responseDecision := hm.AfterLLM(context.Background(), llmResponse)
	if responseDecision.normalizedAction() != HookActionContinue || gotResponse.Response.Content != "original-content" {
		t.Fatalf("AfterLLM = %#v / %#v, want unchanged continue", gotResponse, responseDecision)
	}
	if got := gotResponse.Response.ToolCalls[0].Arguments["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("AfterLLM nested value = %v, want original", got)
	}

	toolRequest := &ToolCallHookRequest{
		Tool:      "original_tool",
		Arguments: map[string]any{"nested": map[string]any{"value": "original"}},
	}
	gotTool, toolDecision := hm.BeforeTool(context.Background(), toolRequest)
	if toolDecision.normalizedAction() != HookActionContinue || gotTool.Tool != "original_tool" ||
		gotTool.HookResult != nil {
		t.Fatalf("BeforeTool = %#v / %#v, want unchanged continue", gotTool, toolDecision)
	}
	if got := gotTool.Arguments["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("BeforeTool nested value = %v, want original", got)
	}

	afterInput := &ToolResultHookResponse{
		Tool:      "original_tool",
		Arguments: map[string]any{"nested": map[string]any{"value": "original"}},
		Result:    tools.SilentResult("original result"),
	}
	gotAfter, afterDecision := hm.AfterTool(context.Background(), afterInput)
	if afterDecision.normalizedAction() != HookActionContinue || gotAfter.Tool != "original_tool" ||
		gotAfter.Result.ForLLM != "original result" {
		t.Fatalf("AfterTool = %#v / %#v, want unchanged continue", gotAfter, afterDecision)
	}
	if got := gotAfter.Arguments["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("AfterTool nested value = %v, want original", got)
	}

	// The hook inputs were detached from both the caller and manager state.
	if got := toolRequest.Arguments["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("caller nested value = %v, want original", got)
	}
}

type trustedProvenanceHook struct {
	retained *ToolCallHookRequest
}

func (hook *trustedProvenanceHook) BeforeTool(
	_ context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	call.Tool = "rewritten_tool"
	call.Arguments["nested"].(map[string]any)["value"] = "rewritten"
	hook.retained = call
	return call, HookDecision{Action: HookActionModify}, nil
}

func (*trustedProvenanceHook) AfterTool(
	_ context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	result.Tool = "forged_audit_name"
	result.Arguments["nested"].(map[string]any)["value"] = "forged"
	result.Result.ForLLM = "rewritten result"
	return result, HookDecision{Action: HookActionModify}, nil
}

func TestHookManager_TrustedRewriteProvenanceAndAfterToolIdentity(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()
	hook := &trustedProvenanceHook{}
	if err := hm.Mount(NamedHook("trusted-rewrite", hook)); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	before, decision := hm.BeforeTool(context.Background(), &ToolCallHookRequest{
		Tool:      "original_tool",
		Arguments: map[string]any{"nested": map[string]any{"value": "original"}},
	})
	if decision.normalizedAction() != HookActionContinue || before.Tool != "rewritten_tool" {
		t.Fatalf("BeforeTool = %#v / %#v, want trusted rewrite", before, decision)
	}
	provenance := before.policyHookProvenance()
	if provenance.Name != "trusted-rewrite" || provenance.Source != "in_process" || !provenance.Trusted {
		t.Fatalf("provenance = %#v, want trusted in-process hook", provenance)
	}

	// A hook retaining and mutating its returned graph cannot change the
	// manager-owned accepted request.
	hook.retained.Arguments["nested"].(map[string]any)["value"] = "late mutation"
	if got := before.Arguments["nested"].(map[string]any)["value"]; got != "rewritten" {
		t.Fatalf("accepted nested value = %v, want rewritten", got)
	}

	after, afterDecision := hm.AfterTool(context.Background(), &ToolResultHookResponse{
		Tool:      before.Tool,
		Arguments: before.Arguments,
		Result:    tools.SilentResult("original result"),
	})
	if afterDecision.normalizedAction() != HookActionContinue {
		t.Fatalf("AfterTool decision = %#v, want continue", afterDecision)
	}
	if after.Tool != "rewritten_tool" {
		t.Fatalf("AfterTool tool = %q, want authorized identity", after.Tool)
	}
	if got := after.Arguments["nested"].(map[string]any)["value"]; got != "rewritten" {
		t.Fatalf("AfterTool arguments = %v, want authorized identity", got)
	}
	if after.Result.ForLLM != "rewritten result" {
		t.Fatalf("AfterTool result = %q, want trusted result rewrite", after.Result.ForLLM)
	}
}

type untrustedDenyAbortHook struct{}

func (untrustedDenyAbortHook) BeforeTool(
	_ context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	return call, HookDecision{Action: HookActionDenyTool, Reason: "narrowed"}, nil
}

func (untrustedDenyAbortHook) AfterTool(
	_ context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return result, HookDecision{Action: HookActionContinue}, nil
}

func (untrustedDenyAbortHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	return req, HookDecision{Action: HookActionAbortTurn}, nil
}

func (untrustedDenyAbortHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp, HookDecision{Action: HookActionContinue}, nil
}

func TestHookManager_UntrustedDenyAndAbortRemainEffective(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()
	if err := hm.Mount(UntrustedNamedHook("narrowing", untrustedDenyAbortHook{})); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	_, llmDecision := hm.BeforeLLM(context.Background(), &LLMHookRequest{})
	if llmDecision.normalizedAction() != HookActionAbortTurn {
		t.Fatalf("BeforeLLM decision = %#v, want abort", llmDecision)
	}
	_, toolDecision := hm.BeforeTool(context.Background(), &ToolCallHookRequest{
		Tool:      "tool",
		Arguments: map[string]any{},
	})
	if toolDecision.normalizedAction() != HookActionDenyTool || toolDecision.Reason != "narrowed" {
		t.Fatalf("BeforeTool decision = %#v, want deny", toolDecision)
	}
}

func TestHookRegistration_TrustDefaultsAndHelpers(t *testing.T) {
	if got := (HookRegistration{}).Trust; got != HookTrustUntrusted {
		t.Fatalf("raw trust = %v, want untrusted", got)
	}
	if got := NamedHook("trusted", struct{}{}).Trust; got != HookTrustTrusted {
		t.Fatalf("NamedHook trust = %v, want trusted", got)
	}
	if got := UntrustedNamedHook("untrusted", struct{}{}).Trust; got != HookTrustUntrusted {
		t.Fatalf("UntrustedNamedHook trust = %v, want untrusted", got)
	}
}
