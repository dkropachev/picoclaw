package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// =============================================================================
// Mock Providers for turn_coord Tests
// =============================================================================

// simpleConvProvider returns a simple text response without tools
type simpleConvProvider struct{}

func TestWorkflowAgentUsageNilSafetyAndAdmissionErrors(t *testing.T) {
	if err := admitWorkflowAgentCall(nil); err != nil {
		t.Fatalf("nil admission error = %v", err)
	}
	blocked := errors.New("quota exhausted")
	if err := admitWorkflowAgentCall(
		func() error { return blocked },
	); !errors.Is(err, workflows.ErrAgentCallNotAdmitted) ||
		!errors.Is(err, blocked) {
		t.Fatalf("blocked admission error = %v", err)
	}

	var accumulator *workflowAgentUsageAccumulator
	if err := accumulator.Observe(workflows.AgentUsage{}); err != nil {
		t.Fatalf("nil accumulator observe error = %v", err)
	}
	if snapshot := accumulator.Snapshot(); snapshot == nil || len(snapshot) != 0 {
		t.Fatalf("nil accumulator snapshot = %#v", snapshot)
	}
	if err := accumulator.Err(); err != nil {
		t.Fatalf("nil accumulator error = %v", err)
	}
	if usage, ok := workflowAgentUsageFromResponse("model", nil, time.Second); !ok ||
		usage.Model != "model" || usage.ProviderCalls != 1 || usage.UsageReportedCalls != 0 ||
		usage.LatencyMillis != 1000 {
		t.Fatalf("nil response usage = (%#v, %t)", usage, ok)
	}
	usage, ok := workflowAgentUsageFromResponse(" model ", &providers.LLMResponse{}, -time.Second)
	if !ok || usage.Model != "model" || usage.LatencyMillis != 0 ||
		usage.ProviderCalls != 1 || usage.UsageReportedCalls != 0 {
		t.Fatalf("metadata-only response usage = (%#v, %t)", usage, ok)
	}
	usage, ok = workflowAgentUsageFromResponse("concrete", &providers.LLMResponse{
		Usage: &providers.UsageInfo{CompletionTokens: 10, ReasoningTokens: 7},
	}, time.Second)
	if !ok || usage.Model != "concrete" || usage.CompletionTokens != 10 || usage.ReasoningTokens != 7 ||
		usage.ProviderCalls != 1 || usage.UsageReportedCalls != 1 {
		t.Fatalf("reasoning response usage = (%#v, %t)", usage, ok)
	}
	usage, ok = workflowAgentUsageFromResponse("estimated", &providers.LLMResponse{
		Usage: &providers.UsageInfo{
			PromptTokens: 6, CompletionTokens: 2, TotalTokens: 8, Estimated: true,
		},
	}, time.Second)
	if !ok || usage.ProviderCalls != 1 || usage.UsageReportedCalls != 0 ||
		usage.PromptTokens != 6 || usage.TotalTokens != 8 {
		t.Fatalf("estimated response usage = (%#v, %t)", usage, ok)
	}
	if cloned := cloneWorkflowAgentUsage(nil); cloned == nil || len(cloned) != 0 {
		t.Fatalf("nil usage clone = %#v", cloned)
	}

	var state *turnState
	if err := state.observeWorkflowAgentResponse("model", &providers.LLMResponse{}, time.Second); err != nil {
		t.Fatalf("nil turn usage observe error = %v", err)
	}
	if snapshot := state.workflowAgentUsageSnapshot(); snapshot == nil || len(snapshot) != 0 {
		t.Fatalf("nil turn usage snapshot = %#v", snapshot)
	}
	if err := state.workflowAgentUsageError(); err != nil {
		t.Fatalf("nil turn usage error = %v", err)
	}
}

func TestWorkflowAgentUsageAccumulatorRejectsNegativeAndOverflow(t *testing.T) {
	accumulator := newWorkflowAgentUsageAccumulator(nil)
	maximum := int(^uint(0) >> 1)
	if err := accumulator.Observe(workflows.AgentUsage{
		Model: "model", ProviderCalls: 1, UsageReportedCalls: 1,
		PromptTokens: maximum, TotalTokens: maximum,
	}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Observe(workflows.AgentUsage{
		Model: "model", ProviderCalls: 1, UsageReportedCalls: 1,
		PromptTokens: 1, TotalTokens: 1,
	}); err == nil || !strings.Contains(err.Error(), "aggregation") {
		t.Fatalf("overflow Observe() error = %v", err)
	}
	usage := accumulator.Snapshot()
	if len(usage) != 1 || usage[0].ProviderCalls != 2 || usage[0].UsageReportedCalls != 1 ||
		usage[0].PromptTokens != maximum || accumulator.Complete() {
		t.Fatalf("overflow changed aggregate: %#v", usage)
	}

	negative := newWorkflowAgentUsageAccumulator(nil)
	if err := negative.Observe(workflows.AgentUsage{
		Model: "model", ProviderCalls: -1,
	}); err == nil {
		t.Fatal("negative provider calls were accepted")
	}
	if usage := negative.Snapshot(); len(usage) != 0 {
		t.Fatalf("negative usage was retained: %#v", usage)
	}

	for name, invalid := range map[string]workflows.AgentUsage{
		"reported calls": {
			Model: "model", ProviderCalls: 1, UsageReportedCalls: 2,
		},
		"cached subset": {
			Model: "model", ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 1, CachedTokens: 2, TotalTokens: 1,
		},
		"reasoning subset": {
			Model: "model", ProviderCalls: 1, UsageReportedCalls: 1,
			CompletionTokens: 1, ReasoningTokens: 2, TotalTokens: 1,
		},
		"total mismatch": {
			Model: "model", ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 2, CompletionTokens: 3, TotalTokens: 4,
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := newWorkflowAgentUsageAccumulator(nil)
			if err := candidate.Observe(invalid); err == nil {
				t.Fatalf("invalid per-call usage was accepted: %#v", invalid)
			}
			if snapshot := candidate.Snapshot(); len(snapshot) != 1 ||
				snapshot[0].ProviderCalls != 1 || snapshot[0].UsageReportedCalls != 0 ||
				snapshot[0].PromptTokens != 0 || snapshot[0].CompletionTokens != 0 ||
				snapshot[0].TotalTokens != 0 || candidate.Complete() {
				t.Fatalf("invalid per-call usage envelope = %#v", snapshot)
			}
		})
	}

	canceling := newWorkflowAgentUsageAccumulator(nil)
	for _, invalid := range []workflows.AgentUsage{
		{Model: "model", ProviderCalls: 1, UsageReportedCalls: 1, PromptTokens: 1, CompletionTokens: 2, TotalTokens: 4},
		{Model: "model", ProviderCalls: 1, UsageReportedCalls: 1, PromptTokens: 1, CompletionTokens: 2, TotalTokens: 2},
	} {
		if err := canceling.Observe(invalid); err == nil {
			t.Fatalf("canceling invalid usage was accepted: %#v", invalid)
		}
	}
	if snapshot := canceling.Snapshot(); len(snapshot) != 1 ||
		snapshot[0].ProviderCalls != 2 || snapshot[0].UsageReportedCalls != 0 ||
		canceling.Complete() {
		t.Fatalf("canceling invalid calls produced a valid aggregate: %#v", snapshot)
	}
}

func (p *simpleConvProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:      "Hello! How can I help you today?",
		FinishReason: "stop",
	}, nil
}

type sequenceProvider struct {
	responses []*providers.LLMResponse
	errors    []error
	callCount int
	mu        sync.Mutex
}

func (p *sequenceProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx := p.callCount
	p.callCount++

	if idx < len(p.errors) && p.errors[idx] != nil {
		return nil, p.errors[idx]
	}
	if idx < len(p.responses) && p.responses[idx] != nil {
		return p.responses[idx], nil
	}
	return &providers.LLMResponse{Content: "ok", FinishReason: "stop"}, nil
}

type nativeSearchCaptureProvider struct {
	lastOpts map[string]any
	messages []providers.Message
	tools    []providers.ToolDefinition
}

func (p *nativeSearchCaptureProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.messages = append([]providers.Message(nil), messages...)
	p.tools = append([]providers.ToolDefinition(nil), tools...)
	p.lastOpts = make(map[string]any, len(opts))
	for k, v := range opts {
		p.lastOpts[k] = v
	}
	return &providers.LLMResponse{
		Content:      "Using native search",
		FinishReason: "stop",
	}, nil
}

func (p *nativeSearchCaptureProvider) SupportsNativeSearch() bool {
	return true
}

// toolCallRespProvider returns a tool call response
type toolCallRespProvider struct {
	toolName  string
	toolArgs  map[string]any
	response  string
	usages    []providers.UsageInfo
	callCount int
	mu        sync.Mutex
}

func (p *toolCallRespProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	count := p.callCount
	p.mu.Unlock()
	var usage *providers.UsageInfo
	if count <= len(p.usages) {
		usageValue := p.usages[count-1]
		usage = &usageValue
	}

	// First call returns a tool call, subsequent calls return final response
	if count == 1 {
		return &providers.LLMResponse{
			Content: "Let me search for that information.",
			ToolCalls: []providers.ToolCall{
				{
					ID:        "call_1",
					Name:      p.toolName,
					Arguments: p.toolArgs,
				},
			},
			FinishReason: "tool_calls",
			Usage:        usage,
		}, nil
	}
	return &providers.LLMResponse{
		Content:      p.response,
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}

func TestRunAgentLoopAccumulatesEveryProviderResponseUsage(t *testing.T) {
	provider := &toolCallRespProvider{
		toolName: "mock_custom",
		toolArgs: map[string]any{},
		response: "finished",
		usages: []providers.UsageInfo{
			{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, CachedTokens: 1},
			{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15, CachedTokens: 2},
		},
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&mockCustomTool{})
	var resultUsage []workflows.AgentUsage
	var observed []workflows.AgentUsage
	opts := makeTestProcessOpts("usage-multi-response")
	opts.resultUsage = &resultUsage
	opts.usageObserver = func(usage workflows.AgentUsage) error {
		observed = append(observed, usage)
		return nil
	}

	response, err := al.runAgentLoop(t.Context(), agent, opts)
	if err != nil || response != "finished" {
		t.Fatalf("runAgentLoop() response = %q, error = %v", response, err)
	}
	wantResult := []workflows.AgentUsage{{
		Model:              "test-model",
		ProviderCalls:      2,
		UsageReportedCalls: 2,
		PromptTokens:       20,
		CompletionTokens:   5,
		TotalTokens:        25,
		CachedTokens:       3,
	}}
	if !reflect.DeepEqual(resultUsage, wantResult) {
		t.Fatalf("result usage = %#v, want %#v", resultUsage, wantResult)
	}
	if !reflect.DeepEqual(observed, []workflows.AgentUsage{
		{
			Model:              "test-model",
			ProviderCalls:      1,
			UsageReportedCalls: 1,
			PromptTokens:       8,
			CompletionTokens:   2,
			TotalTokens:        10,
			CachedTokens:       1,
		},
		{
			Model:              "test-model",
			ProviderCalls:      1,
			UsageReportedCalls: 1,
			PromptTokens:       12,
			CompletionTokens:   3,
			TotalTokens:        15,
			CachedTokens:       2,
		},
	}) {
		t.Fatalf("observed usage = %#v, want every provider response", observed)
	}
}

// errorProvider simulates various error conditions
type errorProvider struct {
	errType   string
	callCount int
	mu        sync.Mutex
}

func (p *errorProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	p.mu.Unlock()

	switch p.errType {
	case "timeout":
		return nil, context.DeadlineExceeded
	case "context_length":
		return nil, errors.New("context_length_exceeded")
	case "vision":
		return nil, errors.New("vision_unsupported")
	case "connection_reset":
		return nil, errors.New("connection reset by peer")
	case "broken_pipe":
		return nil, errors.New("broken pipe")
	case "read_tcp":
		return nil, errors.New("read tcp 127.0.0.1:8080: connection reset")
	case "eof":
		return nil, errors.New("EOF")
	case "connection_refused":
		return nil, errors.New("connection refused")
	default:
		return nil, errors.New("unknown error")
	}
}

type failOnceLLMProvider struct {
	err           error
	firstResponse *providers.LLMResponse
	response      string
	callCount     int
	mu            sync.Mutex
}

func (p *failOnceLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	callCount := p.callCount
	p.mu.Unlock()

	if callCount == 1 {
		return p.firstResponse, p.err
	}
	return &providers.LLMResponse{
		Content:      p.response,
		FinishReason: "stop",
	}, nil
}

// =============================================================================
// Test Helper Functions
// =============================================================================

func newTurnCoordTestLoop(t *testing.T, provider providers.LLMProvider) (*AgentLoop, *AgentInstance, func()) {
	t.Helper()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	return al, agent, func() {
		al.Close()
	}
}

func makeTestProcessOpts(sessionKey string) processOptions {
	return processOptions{
		SessionKey:      sessionKey,
		Channel:         "cli",
		ChatID:          "test-chat",
		UserMessage:     "test message",
		DefaultResponse: "I couldn't process your request.",
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       false,
	}
}

type saveFailingSessionStore struct {
	session.SessionStore
	err error
}

func (s *saveFailingSessionStore) Save(_ string) error {
	return s.err
}

// =============================================================================
// Pipeline Method Tests: SetupTurn
// =============================================================================

func TestPipeline_SetupTurn_BasicInitialization(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if exec == nil {
		t.Fatal("expected non-nil turnExecution")
	}
	if len(exec.messages) == 0 {
		t.Error("expected messages to be populated")
	}
	if exec.iteration != 0 {
		t.Errorf("expected iteration 0, got %d", exec.iteration)
	}
}

// =============================================================================
// Pipeline Method Tests: CallLLM
// =============================================================================

func TestPipeline_CallLLM_SimpleResponse(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlBreak {
		t.Errorf("expected ControlBreak, got %v", ctrl)
	}
	if exec.response == nil {
		t.Fatal("expected non-nil response")
	}
	if exec.response.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestPipeline_SetupTurn_ModelNameDoesNotUseFallbackAliasBeforeFallback(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	agent.Model = "primary-model"
	agent.Candidates = []providers.FallbackCandidate{
		strictTestCandidate(al.cfg, agent, provider, "primary-model", "gpt-5.4"),
		strictTestCandidate(al.cfg, agent, provider, "fallback-model", "claude-sonnet"),
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if exec.llmModelName != "primary-model" {
		t.Fatalf("exec.llmModelName = %q, want %q", exec.llmModelName, "primary-model")
	}
}

func TestPipeline_CallLLM_UsesSuccessfulFallbackAlias(t *testing.T) {
	tests := []struct {
		name          string
		primaryAlias  string
		primaryModel  string
		fallbackAlias string
		fallbackModel string
	}{
		{
			name:          "identity distinguishes aliases for the same model",
			primaryAlias:  "primary",
			primaryModel:  "gpt-5.4",
			fallbackAlias: "secondary",
			fallbackModel: "gpt-5.4",
		},
		{
			name:          "fallback uses a different model",
			primaryAlias:  "primary-model",
			primaryModel:  "gpt-5.4",
			fallbackAlias: "claude",
			fallbackModel: "claude-sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &sequenceProvider{
				errors: []error{
					errors.New("status: 429 - rate limit exceeded"),
					nil,
				},
				responses: []*providers.LLMResponse{
					nil,
					{Content: "fallback answer", FinishReason: "stop"},
				},
			}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()

			agent.Model = tt.primaryAlias
			agent.Candidates = []providers.FallbackCandidate{
				strictTestCandidate(
					al.cfg,
					agent,
					provider,
					tt.primaryAlias,
					tt.primaryModel,
				),
				strictTestCandidate(
					al.cfg,
					agent,
					provider,
					tt.fallbackAlias,
					tt.fallbackModel,
				),
			}
			al.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)

			pipeline := NewPipeline(al)
			ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
				turnID:  "turn-1",
				context: newTurnContext(nil, nil, nil),
			})

			exec, err := pipeline.SetupTurn(context.Background(), ts)
			if err != nil {
				t.Fatalf("SetupTurn failed: %v", err)
			}

			ctrl, err := pipeline.CallLLM(
				context.Background(),
				context.Background(),
				ts,
				exec,
				1,
			)
			if err != nil {
				t.Fatalf("CallLLM failed: %v", err)
			}
			if ctrl != ControlBreak {
				t.Fatalf("expected ControlBreak, got %v", ctrl)
			}
			if exec.llmModelName != tt.fallbackAlias {
				t.Fatalf(
					"exec.llmModelName = %q, want %q",
					exec.llmModelName,
					tt.fallbackAlias,
				)
			}
		})
	}
}

func TestPipeline_SetupTurn_UsesLightCandidateDisplayName(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	agent.Model = "primary-model"
	agent.Candidates = []providers.FallbackCandidate{
		strictTestCandidate(al.cfg, agent, provider, "primary-model", "gpt-5.4"),
	}
	agent.LightCandidates = []providers.FallbackCandidate{
		strictTestCandidate(al.cfg, agent, provider, "light-model", "gpt-5.4-mini"),
	}
	agent.Router = routing.New(routing.RouterConfig{LightModel: "light-model", Threshold: 1})

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session")
	opts.UserMessage = ""
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if !exec.usedLight {
		t.Fatal("expected light routing to be used")
	}
	if exec.llmModelName != "light-model" {
		t.Fatalf("exec.llmModelName = %q, want %q", exec.llmModelName, "light-model")
	}
}

func TestRunTurn_FinalizeSaveErrorEmitsErrorTurnEnd(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	saveErr := errors.New("session save failed")
	agent.Sessions = &saveFailingSessionStore{
		SessionStore: session.NewSessionManager(""),
		err:          saveErr,
	}

	sub := al.SubscribeEvents(8)
	defer al.UnsubscribeEvents(sub.ID)

	if _, err := al.ProcessDirect(context.Background(), "hello", "session-save-fail"); err == nil {
		t.Fatal("expected ProcessDirect to fail")
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventKindTurnEnd {
				continue
			}
			payload, ok := evt.Payload.(TurnEndPayload)
			if !ok {
				t.Fatalf("TurnEnd payload type = %T", evt.Payload)
			}
			if payload.Status != TurnEndStatusError {
				t.Fatalf("TurnEnd status = %q, want %q", payload.Status, TurnEndStatusError)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for turn_end event")
		}
	}
}

func TestPipeline_CallLLM_WithToolCall(t *testing.T) {
	provider := &toolCallRespProvider{
		toolName: "web_search",
		toolArgs: map[string]any{"query": "test"},
		response: "Found information about test.",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlToolLoop {
		t.Errorf("expected ControlToolLoop, got %v", ctrl)
	}
	if len(exec.normalizedToolCalls) == 0 {
		t.Fatal("expected tool calls")
	}
	if exec.normalizedToolCalls[0].Name != "web_search" {
		t.Errorf("expected tool name 'web_search', got %q", exec.normalizedToolCalls[0].Name)
	}
}

func TestPipeline_CallLLM_UsesNativeSearchWithoutClientWebSearchTool(t *testing.T) {
	provider := &nativeSearchCaptureProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	if _, ok := agent.Tools.Get("web_search"); ok {
		t.Fatal("expected no client-side web_search tool to be registered")
	}

	al.cfg.Tools.Web.Enabled = true
	al.cfg.Tools.Web.PreferNative = true

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	if got, _ := provider.lastOpts["native_search"].(bool); !got {
		t.Fatalf("expected native_search=true, got %#v", provider.lastOpts["native_search"])
	}
}

func TestPipeline_CallLLM_TimeoutRetry(t *testing.T) {
	errorPrv := &errorProvider{errType: "timeout"}
	al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// Should retry and eventually fail after max retries
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err == nil {
		t.Error("expected error after retries")
	}
}

func TestPipeline_CallLLM_HTTP5xxRetry(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &failOnceLLMProvider{
		err:      errors.New("API request failed:\n  Status: 500\n  Body:   internal server error"),
		response: "Recovered from server error",
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       1,
				LLMRetryBackoffSecs: 1,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("expected HTTP 500 retry to recover, got error: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	if exec.finalContent != "Recovered from server error" {
		t.Fatalf("finalContent = %q, want recovered response", exec.finalContent)
	}
	if provider.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", provider.callCount)
	}
}

func TestPipeline_CallLLM_SafetyFilterRetry(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &failOnceLLMProvider{
		err:      errors.New(`API request failed: Status: 400 Body: {"code":"security_violation"}`),
		response: "Recovered with the bounded retry",
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       1,
				LLMRetryBackoffSecs: 1,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("expected safety-filter retry to recover, got error: %v", err)
	}
	if ctrl != ControlBreak || exec.finalContent != "Recovered with the bounded retry" {
		t.Fatalf("control = %v, finalContent = %q", ctrl, exec.finalContent)
	}
	if provider.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", provider.callCount)
	}
}

func TestPipeline_CallLLM_ResponseSafetyFinishReasonRetry(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &failOnceLLMProvider{
		firstResponse: &providers.LLMResponse{FinishReason: "content_filter"},
		response:      "Recovered after response-level safety filter",
	}
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: tmpDir, ModelName: "test-model", MaxTokens: 4096,
		MaxToolIterations: 10, MaxLLMRetries: 1, LLMRetryBackoffSecs: 1,
	}}}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("response-safety"), turnEventScope{
		turnID: "turn-response-safety", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil || ctrl != ControlBreak ||
		exec.finalContent != "Recovered after response-level safety filter" || provider.callCount != 2 {
		t.Fatalf(
			"response safety retry control=%v content=%q calls=%d err=%v",
			ctrl,
			exec.finalContent,
			provider.callCount,
			err,
		)
	}
}

func TestSideQuestion_ResponseSafetyFinishReasonRetriesOnce(t *testing.T) {
	provider := &failOnceLLMProvider{
		firstResponse: &providers.LLMResponse{FinishReason: "content_filter"},
		response:      "side question recovered",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.providerFactory = func(*config.ModelConfig) (providers.LLMProvider, string, error) {
		return provider, "test-model", nil
	}
	response, err := al.askSideQuestionWithOptions(
		context.Background(), agent,
		&processOptions{Dispatch: DispatchRequest{SessionKey: "side-response-safety"}, NoHistory: true},
		"Review this evidence",
		sideQuestionExecutionOptions{disablePromptCache: true, skipHooks: true},
	)
	if err != nil || response != "side question recovered" || provider.callCount != 2 {
		t.Fatalf("side response safety response=%q calls=%d err=%v", response, provider.callCount, err)
	}
}

func TestPipeline_CallLLM_ContextLengthError(t *testing.T) {
	errorPrv := &errorProvider{errType: "context_length"}
	al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// Should trigger context compression and retry
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	// May succeed after compression or fail - either is acceptable
	t.Logf("CallLLM result after context error: err=%v", err)
}

func TestPipeline_CallLLM_NetworkErrorRetry(t *testing.T) {
	testCases := []struct {
		name    string
		errType string
	}{
		{"connection_reset", "connection_reset"},
		{"broken_pipe", "broken_pipe"},
		{"read_tcp", "read_tcp"},
		{"eof", "eof"},
		{"connection_refused", "connection_refused"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errorPrv := &errorProvider{errType: tc.errType}
			al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
			defer cleanup()

			pipeline := NewPipeline(al)
			ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
				turnID:  "turn-1",
				context: newTurnContext(nil, nil, nil),
			})

			exec, err := pipeline.SetupTurn(context.Background(), ts)
			if err != nil {
				t.Fatalf("SetupTurn failed: %v", err)
			}

			_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
			if err == nil {
				t.Error("expected error after network error retries")
			}
		})
	}
}

func TestPipeline_CallLLM_RetryConfigRespected(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       3,
				LLMRetryBackoffSecs: 1,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &errorProvider{errType: "connection_reset"}
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	start := time.Now()
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error after retries")
	}

	expectedMinTime := 3 * time.Second
	if elapsed < expectedMinTime {
		t.Errorf("expected at least %v of backoff, got %v", expectedMinTime, elapsed)
	}
}

func TestPipeline_CallLLM_RetryCountLimit(t *testing.T) {
	tmpDir := t.TempDir()

	counterPrv := &countingErrorProvider{errType: "connection_reset", targetCalls: 5}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       2,
				LLMRetryBackoffSecs: 0,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, counterPrv)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err == nil {
		t.Error("expected error after retries")
	}

	if counterPrv.callCount != 3 {
		t.Errorf("expected exactly 3 calls (1 initial + 2 retries), got %d", counterPrv.callCount)
	}
}

type countingErrorProvider struct {
	errType     string
	targetCalls int
	callCount   int
	mu          sync.Mutex
}

func (p *countingErrorProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	p.mu.Unlock()
	return nil, errors.New("connection reset by peer")
}

// =============================================================================
// Pipeline Method Tests: ExecuteTools
// =============================================================================

func TestPipeline_ExecuteTools_NoTools(t *testing.T) {
	// Provider returns no tool calls, so ExecuteTools should not be called
	// This test verifies the ControlBreak path from CallLLM
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// First CallLLM returns ControlBreak (no tools)
	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}

	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	// No tools to execute, Finalize should be called directly
}

// =============================================================================
// runTurn Integration Tests
// =============================================================================

func TestRunTurn_SimpleConversation(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-simple")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-simple",
		context: newTurnContext(nil, nil, nil),
	})

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
	if result.finalContent == "" {
		t.Error("expected non-empty finalContent")
	}
}

func TestRunTurn_MaxIterations(t *testing.T) {
	// Provider always returns tool calls, should hit max iterations
	provider := &toolCallRespProvider{
		toolName: "search",
		toolArgs: map[string]any{"q": "x"},
		response: "done",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	// Override max iterations to 2
	agent.MaxIterations = 2

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-maxiter")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-maxiter",
		context: newTurnContext(nil, nil, nil),
	})

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	// Should complete due to max iterations
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
}

func TestRunTurn_HardAbort(t *testing.T) {
	// Provider simulates a slow response, but we'll abort mid-turn
	slowProvider := &slowMockProvider{delay: 10 * time.Second}
	al, agent, cleanup := newTurnCoordTestLoop(t, slowProvider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-abort")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-abort",
		context: newTurnContext(nil, nil, nil),
	})

	// Run in goroutine with abort after short delay
	done := make(chan struct{})

	go func() {
		al.runTurn(context.Background(), ts, pipeline)
		close(done)
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Request hard abort
	ts.requestHardAbort()

	// Wait for runTurn to complete
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runTurn did not complete after abort")
	}
}

func TestRunTurn_SteeringMessageInjection(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-steering")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-steering",
		context: newTurnContext(nil, nil, nil),
	})

	// Enqueue steering message before runTurn
	steeringMsg := providers.Message{
		Role:    "user",
		Content: "Steering message",
	}
	al.Steer(steeringMsg)

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
	// Steering message should have been injected
}

func TestRunTurn_GracefulInterrupt(t *testing.T) {
	provider := &toolCallRespProvider{
		toolName: "search",
		toolArgs: map[string]any{"q": "test"},
		response: "Final response after interrupt",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-graceful")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-graceful",
		context: newTurnContext(nil, nil, nil),
	})

	// Run in goroutine with graceful interrupt after first iteration
	done := make(chan struct{})
	var result turnResult

	go func() {
		result, _ = al.runTurn(context.Background(), ts, pipeline)
		close(done)
	}()

	// Give it a moment to start first iteration
	time.Sleep(50 * time.Millisecond)

	// Request graceful interrupt
	ts.requestGracefulInterrupt("Please stop")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn did not complete after graceful interrupt")
	}

	// Should complete gracefully
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
}

// =============================================================================
// turnState Tests
// =============================================================================

func TestTurnState_GracefulInterruptRequested(t *testing.T) {
	ts := &turnState{
		gracefulInterrupt:     false,
		gracefulInterruptHint: "",
	}

	// Initially should not be requested
	requested, _ := ts.gracefulInterruptRequested()
	if requested {
		t.Error("expected no interrupt initially")
	}

	// Request interrupt
	ts.requestGracefulInterrupt("test hint")

	requested, hint := ts.gracefulInterruptRequested()
	if !requested {
		t.Error("expected interrupt to be requested")
	}
	if hint != "test hint" {
		t.Errorf("expected hint 'test hint', got %q", hint)
	}
}

func TestTurnState_HardAbortRequested(t *testing.T) {
	ts := &turnState{
		hardAbort: false,
	}

	if ts.hardAbortRequested() {
		t.Error("expected no hard abort initially")
	}

	ts.requestHardAbort()

	if !ts.hardAbortRequested() {
		t.Error("expected hard abort to be requested")
	}
}

func TestTurnState_SkillContextSnapshotsTrackLatestSuccessfulPath(t *testing.T) {
	ts := &turnState{}

	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, []string{"skill-a"})
	ts.recordSkillContextSnapshot(skillContextTriggerContextRetryRebuild, []string{"skill-b", "skill-c"})

	if got := ts.attemptedSkillsSnapshot(); len(got) != 3 || got[0] != "skill-a" || got[1] != "skill-b" ||
		got[2] != "skill-c" {
		t.Fatalf("attemptedSkillsSnapshot = %v, want [skill-a skill-b skill-c]", got)
	}

	if got := ts.latestSkillContextSnapshot(); len(got) != 2 || got[0] != "skill-b" || got[1] != "skill-c" {
		t.Fatalf("latestSkillContextSnapshot = %v, want [skill-b skill-c]", got)
	}

	snapshots := ts.skillContextSnapshotsSnapshot()
	if len(snapshots) != 2 {
		t.Fatalf("len(skillContextSnapshotsSnapshot()) = %d, want 2", len(snapshots))
	}
	if snapshots[0].Sequence != 1 || snapshots[0].Trigger != skillContextTriggerInitialBuild {
		t.Fatalf("snapshots[0] = %+v, want sequence=1 trigger=%q", snapshots[0], skillContextTriggerInitialBuild)
	}
	if snapshots[1].Sequence != 2 || snapshots[1].Trigger != skillContextTriggerContextRetryRebuild {
		t.Fatalf("snapshots[1] = %+v, want sequence=2 trigger=%q", snapshots[1], skillContextTriggerContextRetryRebuild)
	}
}
