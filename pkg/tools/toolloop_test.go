package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type orderedToolLoopProvider struct {
	calls int
}

func (provider *orderedToolLoopProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	if provider.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
			{ID: "first", Name: "ordered", Arguments: map[string]any{"value": "first"}},
			{ID: "second", Name: "ordered", Arguments: map[string]any{"value": "second"}},
		}}, nil
	}
	return &providers.LLMResponse{Content: "done"}, nil
}

type orderedToolLoopTool struct {
	mu       sync.Mutex
	active   int
	maximum  int
	sequence []string
}

func (tool *orderedToolLoopTool) Name() string        { return "ordered" }
func (tool *orderedToolLoopTool) Description() string { return "records execution order" }
func (tool *orderedToolLoopTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *orderedToolLoopTool) Execute(
	_ context.Context,
	args map[string]any,
) *ToolResult {
	tool.mu.Lock()
	tool.active++
	if tool.active > tool.maximum {
		tool.maximum = tool.active
	}
	tool.sequence = append(tool.sequence, args["value"].(string))
	tool.mu.Unlock()

	// Leave a scheduling window in which a mistakenly parallel loop would
	// overlap both calls and raise maximum above one.
	time.Sleep(10 * time.Millisecond)

	tool.mu.Lock()
	tool.active--
	tool.mu.Unlock()
	return NewToolResult("ok")
}

func TestRunToolLoopSequentialToolCallsPreserveResponseOrder(t *testing.T) {
	provider := &orderedToolLoopProvider{}
	tool := &orderedToolLoopTool{}
	registry := NewToolRegistry()
	registry.Register(tool)

	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider:            provider,
			Model:               "test-model",
			Tools:               registry,
			Policy:              CompatibilityAllowToolPolicy{},
			MaxIterations:       2,
			SequentialToolCalls: true,
		},
		[]providers.Message{{Role: "user", Content: "edit"}},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if result.Content != "done" || result.Iterations != 2 {
		t.Fatalf("RunToolLoop() = %#v", result)
	}
	if tool.maximum != 1 {
		t.Fatalf("maximum concurrent tools = %d, want 1", tool.maximum)
	}
	if len(tool.sequence) != 2 || tool.sequence[0] != "first" || tool.sequence[1] != "second" {
		t.Fatalf("tool sequence = %#v", tool.sequence)
	}
}

type nilToolLoopTool struct{}

func (*nilToolLoopTool) Name() string               { return "ordered" }
func (*nilToolLoopTool) Description() string        { return "returns nil" }
func (*nilToolLoopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*nilToolLoopTool) Execute(context.Context, map[string]any) *ToolResult {
	return nil
}

func TestRunToolLoopSequentialToolCallsNormalizesNilResults(t *testing.T) {
	provider := &orderedToolLoopProvider{}
	registry := NewToolRegistry()
	registry.Register(&nilToolLoopTool{})

	if _, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider:            provider,
			Model:               "test-model",
			Tools:               registry,
			Policy:              CompatibilityAllowToolPolicy{},
			MaxIterations:       2,
			SequentialToolCalls: true,
		},
		[]providers.Message{{Role: "user", Content: "edit"}},
		"",
		"",
	); err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
}

type signatureToolLoopProvider struct {
	calls          int
	secondMessages []providers.Message
}

func (provider *signatureToolLoopProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	if provider.calls == 1 {
		return &providers.LLMResponse{
			ReasoningContent: "reasoning",
			ToolCalls: []providers.ToolCall{{
				ID:               "signed",
				Name:             "ordered",
				Arguments:        map[string]any{"value": "first"},
				ThoughtSignature: "top-signature",
				Function: &providers.FunctionCall{
					Name:             "ordered",
					Arguments:        `{"value":"first"}`,
					ThoughtSignature: "function-signature",
				},
				ExtraContent: &providers.ExtraContent{
					Google: &providers.GoogleExtra{ThoughtSignature: "google-signature"},
				},
			}},
		}, nil
	}
	provider.secondMessages = append([]providers.Message(nil), messages...)
	return &providers.LLMResponse{Content: "done"}, nil
}

func TestRunToolLoopPreservesToolThoughtSignaturesForFollowUp(t *testing.T) {
	provider := &signatureToolLoopProvider{}
	registry := NewToolRegistry()
	registry.Register(&orderedToolLoopTool{})

	if _, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider:            provider,
			Model:               "test-model",
			Tools:               registry,
			Policy:              CompatibilityAllowToolPolicy{},
			MaxIterations:       2,
			SequentialToolCalls: true,
		},
		[]providers.Message{{Role: "user", Content: "edit"}},
		"",
		"",
	); err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if len(provider.secondMessages) != 3 {
		t.Fatalf("follow-up messages = %#v", provider.secondMessages)
	}
	assistant := provider.secondMessages[1]
	if assistant.ReasoningContent != "reasoning" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant follow-up = %#v", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ThoughtSignature != "top-signature" || call.Function == nil ||
		call.Function.ThoughtSignature != "function-signature" ||
		call.ExtraContent == nil || call.ExtraContent.Google == nil ||
		call.ExtraContent.Google.ThoughtSignature != "google-signature" {
		t.Fatalf("preserved tool call = %#v", call)
	}
}

type privateLoggingTool struct{}

func (*privateLoggingTool) Name() string               { return "private_log" }
func (*privateLoggingTool) Description() string        { return "returns a private error" }
func (*privateLoggingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*privateLoggingTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	return ErrorResult("rejected private value " + args["value"].(string))
}

type privateLoggingProvider struct {
	calls  int
	canary string
	path   string
}

func (provider *privateLoggingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	if provider.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
			{
				ID:        "private-read",
				Name:      "read_file",
				Arguments: map[string]any{"path": provider.path},
			},
			{
				ID:        "private-call",
				Name:      "private_log",
				Arguments: map[string]any{"value": provider.canary},
			},
		}}, nil
	}
	return &providers.LLMResponse{Content: "done"}, nil
}

func TestRunToolLoopSuppressesArgumentsAndResultErrorsFromRegistryLogs(t *testing.T) {
	const canary = "private-patch-canary-97a404"
	workspace := t.TempDir()
	privatePath := canary + ".txt"
	if err := os.WriteFile(filepath.Join(workspace, privatePath), []byte("safe\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	logPath := filepath.Join(workspace, "toolloop.log")
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	defer logger.SetLevel(initialLevel)
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	loggingEnabled := true
	defer func() {
		if loggingEnabled {
			logger.DisableFileLogging()
		}
	}()

	registry := NewToolRegistry()
	registry.Register(NewReadFileLinesTool(workspace, true, MaxReadFileSize))
	registry.Register(&privateLoggingTool{})
	_, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: &privateLoggingProvider{
				canary: canary,
				path:   privatePath,
			},
			Model:                 "test-model",
			Tools:                 registry,
			Policy:                CompatibilityAllowToolPolicy{},
			MaxIterations:         2,
			SequentialToolCalls:   true,
			SuppressToolArguments: true,
		},
		[]providers.Message{{Role: "user", Content: "edit"}},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	logger.DisableFileLogging()
	loggingEnabled = false
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	if strings.Contains(string(logged), canary) {
		t.Fatalf("suppressed tool value entered logs: %s", logged)
	}
	if !strings.Contains(string(logged), "private_log") {
		t.Fatalf("tool name missing from suppressed logs: %s", logged)
	}
}
