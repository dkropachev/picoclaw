package tools

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type closeoutToolLoopProvider struct {
	mu        sync.Mutex
	responses []*providers.LLMResponse
	err       error
	cancel    context.CancelFunc
	calls     int
	messages  [][]providers.Message
}

func (provider *closeoutToolLoopProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.messages = append(provider.messages, append([]providers.Message(nil), messages...))
	provider.calls++
	if provider.cancel != nil {
		provider.cancel()
	}
	if provider.err != nil {
		return nil, provider.err
	}
	if provider.calls > len(provider.responses) {
		return nil, nil
	}
	return provider.responses[provider.calls-1], nil
}

type closeoutToolLoopMediaTool struct {
	handled bool
}

type cancelingToolLoopTool struct {
	cancel context.CancelFunc
}

func (*cancelingToolLoopTool) Name() string               { return "cancel_closeout" }
func (*cancelingToolLoopTool) Description() string        { return "cancels the loop" }
func (*cancelingToolLoopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (tool *cancelingToolLoopTool) Execute(context.Context, map[string]any) *ToolResult {
	tool.cancel()
	return NewToolResult("canceled")
}

func (*closeoutToolLoopMediaTool) Name() string        { return "media_closeout" }
func (*closeoutToolLoopMediaTool) Description() string { return "returns media" }
func (*closeoutToolLoopMediaTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *closeoutToolLoopMediaTool) Execute(context.Context, map[string]any) *ToolResult {
	return &ToolResult{
		ForLLM:          "media result",
		Media:           []string{"media://closeout"},
		ResponseHandled: tool.handled,
	}
}

func TestToolLoopCloseoutCushionProviderFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunToolLoop(
		canceled,
		ToolLoopConfig{MaxIterations: 1},
		nil,
		"",
		"",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled loop = %v", err)
	}
	for _, suppress := range []bool{false, true} {
		providerErr := errors.New("provider failed")
		provider := &closeoutToolLoopProvider{err: providerErr}
		if _, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider, MaxIterations: 1,
				SuppressToolArguments: suppress,
			},
			nil,
			"",
			"",
		); !errors.Is(err, providerErr) {
			t.Fatalf("provider failure suppress=%v: %v", suppress, err)
		}
	}
	if _, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: &closeoutToolLoopProvider{}, MaxIterations: 1,
		},
		nil,
		"",
		"",
	); err == nil {
		t.Fatal("nil provider response succeeded")
	}
	ctx, cancelAfter := context.WithCancel(context.Background())
	provider := &closeoutToolLoopProvider{
		responses: []*providers.LLMResponse{{Content: "ignored"}},
		cancel:    cancelAfter,
	}
	if _, err := RunToolLoop(
		ctx,
		ToolLoopConfig{Provider: provider, MaxIterations: 1},
		nil,
		"",
		"",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-provider cancellation = %v", err)
	}
}

func TestToolLoopCloseoutCushionParallelNoToolsAndMarshalFailure(t *testing.T) {
	provider := &closeoutToolLoopProvider{responses: []*providers.LLMResponse{
		{
			ToolCalls: []providers.ToolCall{
				{ID: "one", Name: "missing", Arguments: map[string]any{"bad": make(chan int)}},
				{ID: "two", Name: "missing", Arguments: nil},
			},
		},
		{Content: "done"},
	}}
	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{Provider: provider, MaxIterations: 2},
		[]providers.Message{{Role: "user", Content: "run"}},
		"",
		"",
	)
	if err != nil || result.Content != "done" || result.Iterations != 2 {
		t.Fatalf("parallel no-tools loop = %#v, %v", result, err)
	}
	if len(provider.messages) != 2 || len(provider.messages[1]) != 4 {
		t.Fatalf("parallel follow-up messages = %#v", provider.messages)
	}
}

func TestToolLoopCloseoutCushionMediaResolverAndHandledMedia(t *testing.T) {
	for _, handled := range []bool{false, true} {
		t.Run(map[bool]string{false: "media forwarded", true: "media handled"}[handled], func(t *testing.T) {
			provider := &closeoutToolLoopProvider{responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{{ID: "media", Name: "media_closeout"}}},
				{Content: "done"},
			}}
			registry := NewToolRegistry()
			registry.Register(&closeoutToolLoopMediaTool{handled: handled})
			resolverCalls := 0
			result, err := RunToolLoop(
				context.Background(),
				ToolLoopConfig{
					Provider: provider, Tools: registry, MaxIterations: 2,
					SequentialToolCalls: true,
					MediaResolver: func(messages []providers.Message) []providers.Message {
						resolverCalls++
						return append([]providers.Message(nil), messages...)
					},
				},
				nil,
				"",
				"",
			)
			if err != nil || result.Content != "done" || resolverCalls != 1 {
				t.Fatalf("media loop = %#v, calls=%d, %v", result, resolverCalls, err)
			}
			toolMessage := provider.messages[1][1]
			if handled == (len(toolMessage.Media) > 0) {
				t.Fatalf("handled=%v media=%#v", handled, toolMessage.Media)
			}
		})
	}
}

func TestToolLoopCloseoutCushionCloneAndIterationBoundary(t *testing.T) {
	if cloneToolLoopExtraContent(nil) != nil {
		t.Fatal("nil extra content cloned")
	}
	value := &providers.ExtraContent{ToolFeedbackExplanation: "feedback"}
	clone := cloneToolLoopExtraContent(value)
	if clone == value || clone.ToolFeedbackExplanation != value.ToolFeedbackExplanation || clone.Google != nil {
		t.Fatalf("extra-content clone = %#v", clone)
	}
	result, err := RunToolLoop(
		nil,
		ToolLoopConfig{MaxIterations: 0},
		nil,
		"",
		"",
	)
	if err != nil || result.Iterations != 0 || result.Content != "" {
		t.Fatalf("zero-iteration loop = %#v, %v", result, err)
	}
}

func TestToolLoopCloseoutCushionSequentialAndFinalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &closeoutToolLoopProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{
			{ID: "one", Name: "cancel_closeout"},
			{ID: "two", Name: "cancel_closeout"},
		},
	}}}
	registry := NewToolRegistry()
	registry.Register(&cancelingToolLoopTool{cancel: cancel})
	if _, err := RunToolLoop(
		ctx,
		ToolLoopConfig{
			Provider: provider, Tools: registry, MaxIterations: 1,
			SequentialToolCalls: true,
		},
		nil,
		"",
		"",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("sequential cancellation = %v", err)
	}
	canceled, cancelFinal := context.WithCancel(context.Background())
	cancelFinal()
	if _, err := RunToolLoop(
		canceled,
		ToolLoopConfig{MaxIterations: 0},
		nil,
		"",
		"",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("final cancellation = %v", err)
	}
}
